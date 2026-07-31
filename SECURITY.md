# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in Hermes Canopy, please report it privately to:

- Email: wojonstech@gmail.com
- GitHub: [Report a vulnerability](https://github.com/coding-hermes/hermes-canopy/security/advisories/new)

Please do not open a public issue for security vulnerabilities.

## Supported Versions

| Version | Supported |
|---------|-----------|
| main branch (development) | Yes |
| Latest release | Yes |

## Security Model

Hermes Canopy is a local-first, single-user application in MVP. Security considerations include:

- **JWT Authentication**: HS256-signed tokens with expiry. Dev mode uses a well-known key; production deployments must rotate.
- **MLS Encryption**: Post-MVP. Group messaging encryption via MLS protocol.
- **Input Validation**: All API endpoints enforce content-length limits and input sanitization.
- **Error Handling**: Internal error details are logged server-side; clients receive generic error messages.

## Known Limitations (MVP)

- No multi-user authentication in MVP (deferred to post-MVP).
- Dev JWT uses a fixed secret — must be rotated for production.
- SQLite card database is local-only with no access controls.
