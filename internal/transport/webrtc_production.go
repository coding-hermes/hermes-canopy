package transport

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	pion "github.com/pion/webrtc/v4"
)

// RegisterPionAdapterFromEnv registers production WebRTC only when explicitly
// enabled. Signaling is supplied by the existing SSE control connection.
func RegisterPionAdapterFromEnv(cm *ConnectionManager, signaling SignalingChannel) (bool, error) {
	if os.Getenv("CANOPY_WEBRTC_ENABLED") != "1" {
		return false, nil
	}
	if signaling == nil {
		signaling = unavailableSignaling{}
	}
	return true, cm.RegisterAdapter(NewWebRTCAdapter(NewPionPeerConnectionFactory(), signaling))
}

// PionPeerConnectionFactory is the production implementation of the
// dependency-neutral WebRTC factory used by WebRTCAdapter.
type PionPeerConnectionFactory struct{}

func NewPionPeerConnectionFactory() PeerConnectionFactory { return &PionPeerConnectionFactory{} }

func (f *PionPeerConnectionFactory) NewPeerConnection(_ context.Context, cfg WebRTCConfig) (PeerConnection, error) {
	ice := make([]pion.ICEServer, 0, len(cfg.ICEServers))
	for _, server := range cfg.ICEServers {
		ice = append(ice, pion.ICEServer{URLs: append([]string(nil), server.URLs...), Username: server.Username, Credential: server.Credential})
	}
	pc, err := pion.NewPeerConnection(pion.Configuration{ICEServers: ice})
	if err != nil {
		return nil, err
	}
	p := &pionPeerConnection{pc: pc}
	pc.OnConnectionStateChange(func(state pion.PeerConnectionState) { p.connectionStateChanged(state) })
	pc.OnDataChannel(p.attachDataChannel)
	return p, nil
}

type pionPeerConnection struct {
	pc *pion.PeerConnection

	mu        sync.RWMutex
	dc        *pion.DataChannel
	onState   func(PeerConnectionState)
	onICE     func(ICECandidate)
	onMessage func([]byte)
}

func (p *pionPeerConnection) CreateOffer(_ context.Context) (SessionDescription, error) {
	p.mu.Lock()
	if p.dc == nil {
		dc, err := p.pc.CreateDataChannel("canopy", nil)
		if err != nil {
			p.mu.Unlock()
			return SessionDescription{}, err
		}
		p.dc = dc
		p.bindDataChannelLocked(dc)
	}
	p.mu.Unlock()
	d, err := p.pc.CreateOffer(nil)
	return fromPionDescription(d), err
}

func (p *pionPeerConnection) CreateAnswer(_ context.Context) (SessionDescription, error) {
	d, err := p.pc.CreateAnswer(nil)
	return fromPionDescription(d), err
}

func (p *pionPeerConnection) SetLocalDescription(_ context.Context, d SessionDescription) error {
	pd, err := toPionDescription(d)
	if err != nil {
		return err
	}
	return p.pc.SetLocalDescription(pd)
}

func (p *pionPeerConnection) SetRemoteDescription(_ context.Context, d SessionDescription) error {
	pd, err := toPionDescription(d)
	if err != nil {
		return err
	}
	return p.pc.SetRemoteDescription(pd)
}

func (p *pionPeerConnection) AddICECandidate(_ context.Context, c ICECandidate) error {
	return p.pc.AddICECandidate(pion.ICECandidateInit{Candidate: c.Candidate, SDPMid: stringPtr(c.SDPMid), SDPMLineIndex: uint16Ptr(c.SDPMLineIndex)})
}

func (p *pionPeerConnection) OnICECandidate(fn func(ICECandidate)) {
	p.mu.Lock()
	p.onICE = fn
	p.mu.Unlock()
	p.pc.OnICECandidate(func(c *pion.ICECandidate) {
		if c == nil {
			return
		}
		j := c.ToJSON()
		candidate := ICECandidate{Candidate: j.Candidate}
		if j.SDPMid != nil {
			candidate.SDPMid = *j.SDPMid
		}
		if j.SDPMLineIndex != nil {
			candidate.SDPMLineIndex = *j.SDPMLineIndex
		}
		p.mu.RLock()
		cb := p.onICE
		p.mu.RUnlock()
		if cb != nil {
			cb(candidate)
		}
	})
}

func (p *pionPeerConnection) OnConnectionStateChange(fn func(PeerConnectionState)) {
	p.mu.Lock()
	p.onState = fn
	p.mu.Unlock()
}

func (p *pionPeerConnection) OnMessage(fn func([]byte)) {
	p.mu.Lock()
	p.onMessage = fn
	p.mu.Unlock()
}

func (p *pionPeerConnection) Send(ctx context.Context, data []byte) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	// The negotiated DataChannel opens asynchronously after the peer
	// connection reports connected — wait bounded instead of failing.
	for {
		p.mu.RLock()
		dc := p.dc
		open := dc != nil && dc.ReadyState() == pion.DataChannelStateOpen
		p.mu.RUnlock()
		if open {
			return dc.Send(data)
		}
		select {
		case <-ctx.Done():
			return ErrConnectionClosed
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (p *pionPeerConnection) State() PeerConnectionState { return mapPionState(p.pc.ConnectionState()) }
func (p *pionPeerConnection) Close() error               { return p.pc.Close() }

func (p *pionPeerConnection) attachDataChannel(dc *pion.DataChannel) {
	p.mu.Lock()
	p.dc = dc
	p.bindDataChannelLocked(dc)
	p.mu.Unlock()
}

func (p *pionPeerConnection) bindDataChannelLocked(dc *pion.DataChannel) {
	dc.OnOpen(func() {
		p.mu.RLock()
		cb := p.onState
		p.mu.RUnlock()
		if cb != nil {
			cb(PeerStateConnected)
		}
	})
	dc.OnClose(func() {
		p.mu.RLock()
		cb := p.onState
		p.mu.RUnlock()
		if cb != nil {
			cb(PeerStateDisconnected)
		}
	})
	dc.OnMessage(func(msg pion.DataChannelMessage) {
		p.mu.RLock()
		cb := p.onMessage
		p.mu.RUnlock()
		if cb != nil {
			cb(append([]byte(nil), msg.Data...))
		}
	})
}

func (p *pionPeerConnection) connectionStateChanged(state pion.PeerConnectionState) {
	p.mu.RLock()
	cb := p.onState
	p.mu.RUnlock()
	if cb != nil {
		cb(mapPionState(state))
	}
}

func mapPionState(state pion.PeerConnectionState) PeerConnectionState {
	switch state {
	case pion.PeerConnectionStateNew:
		return PeerStateNew
	case pion.PeerConnectionStateConnecting:
		return PeerStateChecking
	case pion.PeerConnectionStateConnected:
		return PeerStateConnected
	case pion.PeerConnectionStateDisconnected:
		return PeerStateDisconnected
	case pion.PeerConnectionStateFailed:
		return PeerStateFailed
	case pion.PeerConnectionStateClosed:
		return PeerStateClosed
	default:
		return PeerStateFailed
	}
}

func fromPionDescription(d pion.SessionDescription) SessionDescription {
	return SessionDescription{Type: d.Type.String(), SDP: d.SDP}
}
func toPionDescription(d SessionDescription) (pion.SessionDescription, error) {
	var typ pion.SDPType
	switch d.Type {
	case "offer":
		typ = pion.SDPTypeOffer
	case "answer":
		typ = pion.SDPTypeAnswer
	case "pranswer":
		typ = pion.SDPTypePranswer
	case "rollback":
		typ = pion.SDPTypeRollback
	default:
		return pion.SessionDescription{}, errors.New("transport: invalid SDP type")
	}
	return pion.SessionDescription{Type: typ, SDP: d.SDP}, nil
}
func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
func uint16Ptr(v uint16) *uint16 { return &v }

var _ PeerConnectionFactory = (*PionPeerConnectionFactory)(nil)
var _ PeerConnection = (*pionPeerConnection)(nil)
