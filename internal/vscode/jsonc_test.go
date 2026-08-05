package vscode

import (
	"encoding/json"
	"testing"
)

func TestStripJSONC(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"already clean JSON is untouched",
			`{"a":1,"b":[1,2,3]}`,
			`{"a":1,"b":[1,2,3]}`,
		},
		{
			"line comment",
			"{\n  // a comment\n  \"a\": 1\n}",
			"{\n  \n  \"a\": 1\n}",
		},
		{
			"block comment",
			`{ "a": 1, /* skip this */ "b": 2 }`,
			`{ "a": 1,  "b": 2 }`,
		},
		{
			"comment marker inside a string is not a comment",
			`{"a": "http://example.com"}`,
			`{"a": "http://example.com"}`,
		},
		{
			"escaped quote inside a string does not end it early",
			`{"a": "she said \"// not a comment\""}`,
			`{"a": "she said \"// not a comment\""}`,
		},
		{
			"trailing comma in an object",
			`{"a": 1, "b": 2, }`,
			`{"a": 1, "b": 2 }`,
		},
		{
			"trailing comma in an array",
			`[1, 2, 3, ]`,
			`[1, 2, 3 ]`,
		},
		{
			"trailing comma across a newline",
			"[1, 2,\n]",
			"[1, 2\n]",
		},
		{
			"comma inside a string near a bracket is left alone",
			`["a, ]"]`,
			`["a, ]"]`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(stripJSONC([]byte(c.in)))
			if got != c.want {
				t.Fatalf("stripJSONC(%q) = %q, want %q", c.in, got, c.want)
			}
			if !json.Valid(stripJSONC([]byte(c.in))) {
				t.Fatalf("stripJSONC(%q) produced invalid JSON: %q", c.in, got)
			}
		})
	}
}

func TestHasComments(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"clean JSON", `{"a": 1}`, false},
		{"line comment", "{\n// x\n}", true},
		{"block comment", `{/* x */}`, true},
		{"// inside a string is not a comment", `{"a": "//not a comment"}`, false},
		{"trailing comma alone does not count as a comment", `{"a": 1, }`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasComments([]byte(c.in)); got != c.want {
				t.Errorf("hasComments(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
