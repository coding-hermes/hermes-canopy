/**
 * Unit tests — node avatar derivation (UI-04 branching tree canvas)
 *
 * Avatars are the only place on the canvas where text sits on a saturated
 * fill, so the contrast assertions here are the real regression guard:
 * every colour in the shared avatar palette must clear WCAG AA with the
 * foreground this module picks for it.
 */

import { describe, it, expect } from 'vitest';
import { getColorForUser } from '../../types/multiUser';
import {
  AA_NORMAL,
  AGENT_AUTHOR_NAME,
  AVATAR_INK,
  AVATAR_LIGHT,
  LOCAL_AUTHOR_ID,
  LOCAL_AUTHOR_NAME,
  UNKNOWN_AUTHOR_NAME,
  authorDisplayName,
  avatarInitials,
  avatarTextColor,
  contrastRatio,
  describeNodeAvatar,
  parseHex,
  relativeLuminance,
} from '../nodeAvatar';

/** The full palette from types/multiUser, reached through the public API. */
const PALETTE_PROBES = [
  'u0', 'u1', 'u2', 'u3', 'u4', 'u5', 'u6', 'u7', 'u8', 'u9',
  'alpha', 'bravo', 'charlie', 'delta', 'echo', 'foxtrot',
];

describe('parseHex', () => {
  it('parses 6-digit hex', () => {
    expect(parseHex('#22d3ee')).toEqual([0x22, 0xd3, 0xee]);
  });

  it('expands 3-digit shorthand', () => {
    expect(parseHex('#fff')).toEqual([255, 255, 255]);
  });

  it('tolerates a missing leading hash and whitespace', () => {
    expect(parseHex('  0b0d17 ')).toEqual([0x0b, 0x0d, 0x17]);
  });

  it('returns null for garbage', () => {
    expect(parseHex('rebeccapurple')).toBeNull();
    expect(parseHex('#12345')).toBeNull();
    expect(parseHex('')).toBeNull();
  });
});

describe('relativeLuminance / contrastRatio', () => {
  it('anchors at the WCAG reference values', () => {
    expect(relativeLuminance('#ffffff')).toBeCloseTo(1, 5);
    expect(relativeLuminance('#000000')).toBeCloseTo(0, 5);
  });

  it('black on white is the maximum 21:1', () => {
    expect(contrastRatio('#000000', '#ffffff')).toBeCloseTo(21, 2);
  });

  it('is symmetric', () => {
    expect(contrastRatio('#22d3ee', '#151830')).toBeCloseTo(
      contrastRatio('#151830', '#22d3ee'),
      6,
    );
  });

  it('a colour against itself is 1:1', () => {
    expect(contrastRatio('#a78bfa', '#a78bfa')).toBeCloseTo(1, 6);
  });
});

describe('avatarTextColor', () => {
  it('puts dark ink on light fills', () => {
    // amber + emerald are far too light for white text
    expect(avatarTextColor('#f59e0b')).toBe(AVATAR_INK);
    expect(avatarTextColor('#10b981')).toBe(AVATAR_INK);
  });

  it('puts light text on dark fills', () => {
    expect(avatarTextColor('#7c3aed')).toBe(AVATAR_LIGHT);
  });

  it('every avatar palette colour clears WCAG AA with its chosen text', () => {
    for (const probe of PALETTE_PROBES) {
      const bg = getColorForUser(probe);
      const fg = avatarTextColor(bg);
      expect(
        contrastRatio(bg, fg),
        `${bg} on ${fg} must clear AA`,
      ).toBeGreaterThanOrEqual(AA_NORMAL);
    }
  });
});

describe('authorDisplayName', () => {
  it('names the local author "You"', () => {
    expect(authorDisplayName(LOCAL_AUTHOR_ID)).toBe(LOCAL_AUTHOR_NAME);
  });

  it('prefers a known identity over any derivation', () => {
    const names = new Map([['u-42', 'Sarah Chen']]);
    expect(authorDisplayName('u-42', { names })).toBe('Sarah Chen');
  });

  it('a known identity wins even for the local author', () => {
    const names = new Map([[LOCAL_AUTHOR_ID, 'Alexis']]);
    expect(authorDisplayName(LOCAL_AUTHOR_ID, { names })).toBe('Alexis');
  });

  it('ignores a blank known identity', () => {
    const names = new Map([['u-42', '   ']]);
    expect(authorDisplayName('u-42', { names })).toBe('U 42');
  });

  it('humanises snake/kebab ids', () => {
    expect(authorDisplayName('demo_user_1')).toBe('Demo User 1');
    expect(authorDisplayName('sarah-chen')).toBe('Sarah Chen');
  });

  it('drops a leading "user" segment as noise', () => {
    expect(authorDisplayName('user-sarah-chen')).toBe('Sarah Chen');
    expect(authorDisplayName('users_marcus')).toBe('Marcus');
  });

  it('keeps a bare "user" id when that is all there is', () => {
    expect(authorDisplayName('user')).toBe('User');
  });

  it('falls back for missing ids', () => {
    expect(authorDisplayName(null)).toBe(UNKNOWN_AUTHOR_NAME);
    expect(authorDisplayName(undefined)).toBe(UNKNOWN_AUTHOR_NAME);
    expect(authorDisplayName('')).toBe(UNKNOWN_AUTHOR_NAME);
  });

  it('labels opaque hex ids as agents when flagged', () => {
    expect(authorDisplayName('deadbeefcafe', { isAgent: true })).toBe(
      AGENT_AUTHOR_NAME,
    );
  });

  it('shortens opaque hex ids for humans', () => {
    expect(authorDisplayName('deadbeefcafe')).toBe('User dead');
  });

  it('caps at three segments so a long id cannot overflow the card', () => {
    expect(authorDisplayName('a_b_c_d_e')).toBe('A B C');
  });
});

describe('avatarInitials', () => {
  it('takes one letter from each of the first two words', () => {
    expect(avatarInitials('Sarah Chen')).toBe('SC');
  });

  it('takes two letters from a single word', () => {
    expect(avatarInitials('Marcus')).toBe('MA');
  });

  it('upper-cases', () => {
    expect(avatarInitials('sarah chen')).toBe('SC');
  });

  it('never exceeds two characters', () => {
    expect(avatarInitials('Alpha Bravo Charlie').length).toBeLessThanOrEqual(2);
  });

  it('degrades to a question mark on empty input', () => {
    expect(avatarInitials('')).toBe('?');
    expect(avatarInitials('   ')).toBe('?');
  });
});

describe('describeNodeAvatar', () => {
  it('is deterministic for a given author', () => {
    const a = describeNodeAvatar('demo_user_1');
    const b = describeNodeAvatar('demo_user_1');
    expect(a).toEqual(b);
  });

  it('reuses the shared presence colour so identity is consistent', () => {
    expect(describeNodeAvatar('demo_user_1').background).toBe(
      getColorForUser('demo_user_1'),
    );
  });

  it('gives different authors different descriptors', () => {
    const a = describeNodeAvatar('sarah-chen');
    const b = describeNodeAvatar('marcus-green');
    expect(a.initials).not.toBe(b.initials);
  });

  it('reports a contrast that clears AA', () => {
    for (const probe of PALETTE_PROBES) {
      expect(describeNodeAvatar(probe).contrast).toBeGreaterThanOrEqual(
        AA_NORMAL,
      );
    }
  });

  it('handles a missing author without throwing', () => {
    const d = describeNodeAvatar(null);
    expect(d.name).toBe(UNKNOWN_AUTHOR_NAME);
    expect(d.initials).toBe('UN');
    expect(d.contrast).toBeGreaterThanOrEqual(AA_NORMAL);
  });

  it('renders the local author as "You" → "YO"', () => {
    const d = describeNodeAvatar(LOCAL_AUTHOR_ID);
    expect(d.name).toBe('You');
    expect(d.initials).toBe('YO');
  });
});
