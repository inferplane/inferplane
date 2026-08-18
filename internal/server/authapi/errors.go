package authapi

import "errors"

// Sentinel errors from pickTeam — their Error() text is written directly into
// the 400/403 response body, so each message is meant to be read verbatim by
// a human at a CLI prompt (ADR-028: `mayu login` surfaces the server's
// message rather than inventing its own).
var (
	errNotEntitled    = errors.New("not entitled to that team")
	errTeamRequired   = errors.New("admin identity must specify --team explicitly")
	errNoTeams        = errors.New("your identity maps to no gateway team; ask an admin for a group mapping")
	errAmbiguousTeams = errors.New("entitled to multiple teams; specify --team")
)
