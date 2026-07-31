package plugin

// MethodToPermission maps an API method to its required permission
// (SPEC-PL-01 §7.5). It returns "" for unknown methods.
func MethodToPermission(method string) Permission {
	switch method {
	case "data.query":
		return PermissionDataRead
	case "data.mutate":
		return PermissionDataWrite
	case "notify":
		return PermissionNotification
	case "calendar.query":
		return PermissionCalendarRead
	case "calendar.create":
		return PermissionCalendarWrite
	case "network.fetch":
		return PermissionNetworkRequest
	default:
		return ""
	}
}

// PermissionGranted reports whether the granted set contains the required
// permission. An empty required permission (unknown method) is never granted
// here — callers resolve unknown methods to ErrAPINotFound first.
func PermissionGranted(granted []Permission, required Permission) bool {
	for _, g := range granted {
		if g == required {
			return true
		}
	}
	return false
}

// CheckPermissionGate is the pure permission gate: given an instance's
// granted permissions and an API method, it returns nil when the call is
// allowed, or the typed ErrPermissionDenied / ErrAPINotFound error.
//
// It does not load the instance — the service layer resolves the instance
// (and its status) before calling this.
func CheckPermissionGate(granted []Permission, method string) error {
	required := MethodToPermission(method)
	if required == "" {
		return ErrAPINotFound
	}
	if !PermissionGranted(granted, required) {
		return &PermissionDeniedError{Method: method, Required: required}
	}
	return nil
}
