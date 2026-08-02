// Package oakdev exists only to embed repo-root assets that Go's embed
// directive cannot reach from a package under cmd/ or internal/ (embed can
// only walk downward from the file that declares it). internal/skill imports
// SkillFS to install skills/oak-dev without needing a copy step that would
// let the two drift.
package oakdev

import "embed"

//go:embed skills
var SkillFS embed.FS
