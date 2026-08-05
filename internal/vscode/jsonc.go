package vscode

// stripJSONC returns data with // line comments, /* */ block comments, and
// trailing commas before a closing } or ] removed, so the result is
// plain-JSON parseable by encoding/json. VS Code's own *.json config files
// (.vscode/launch.json, .vscode/tasks.json) are jsonc, not JSON, and
// encoding/json rejects both - this is the one thing standing between "merge
// with what's already on disk" and pulling in a jsonc dependency, so it is
// deliberately small and single-purpose rather than a general jsonc parser.
//
// Comments are discarded, not preserved: a merge that goes through
// json.RawMessage has nowhere to keep them. Callers that care warn the user
// separately - see hasComments.
func stripJSONC(data []byte) []byte {
	return stripTrailingCommas(stripComments(data))
}

// hasComments reports whether data contains a // or /* comment outside a
// string literal. Stripping comments always shortens the input by at least
// the two marker characters, so a length change is sufficient evidence -
// used only to decide whether the "comments will be lost" warning applies,
// not by stripJSONC itself.
func hasComments(data []byte) bool {
	return len(stripComments(data)) != len(data)
}

// stripComments removes // line comments and /* */ block comments from data,
// leaving string literals - including escaped quotes and embedded slashes -
// untouched.
func stripComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	for i := 0; i < len(data); {
		c := data[i]
		if inString {
			out = append(out, c)
			if c == '\\' && i+1 < len(data) {
				out = append(out, data[i+1])
				i += 2
				continue
			}
			if c == '"' {
				inString = false
			}
			i++
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
			i++
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			i += 2
			for i < len(data) && data[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && (data[i] != '*' || data[i+1] != '/') {
				i++
			}
			i += 2 // past the closing */; harmless if the comment was unterminated
		default:
			out = append(out, c)
			i++
		}
	}
	return out
}

// stripTrailingCommas removes a comma that is followed, modulo whitespace, by
// a closing } or ] - the other jsonc allowance VS Code's own files rely on,
// so re-ordering or commenting out the last property/element doesn't leave a
// dangling comma behind. Assumes comments have already been stripped.
func stripTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			if c == '\\' && i+1 < len(data) {
				out = append(out, data[i+1])
				i++
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			out = append(out, c)
		case ',':
			j := i + 1
			for j < len(data) && isJSONSpace(data[j]) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue // drop the comma
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
