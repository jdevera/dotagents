package skills

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

type Change struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Binary bool   `json:"binary"`
	Diff   string `json:"diff"`
}

// Diff compares bytes, permissions, and link targets. Text output is bounded;
// hashes and the change kind remain available for files too large to display.
func Diff(before, after Tree) []Change {
	names := map[string]bool{}
	for name := range before {
		names[name] = true
	}
	for name := range after {
		names[name] = true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	changes := []Change{}
	for _, name := range ordered {
		old, had := before[name]
		next, has := after[name]
		if had && has && old.Mode == next.Mode && old.Link == next.Link && bytes.Equal(old.Data, next.Data) {
			continue
		}
		change := Change{Path: name, Kind: "modified"}
		if !had {
			change.Kind = "added"
		} else {
			change.Before = fileDigest(old)
		}
		if !has {
			change.Kind = "deleted"
		} else {
			change.After = fileDigest(next)
		}
		change.Binary = !isText(old.Data) || !isText(next.Data)
		var out strings.Builder
		fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", name, name)
		if had && has && old.Mode != next.Mode {
			fmt.Fprintf(&out, "mode %04o -> %04o\n", old.Mode, next.Mode)
		}
		if old.Link != "" || next.Link != "" {
			fmt.Fprintf(&out, "link %q -> %q\n", old.Link, next.Link)
		}
		if change.Binary {
			fmt.Fprintf(&out, "Binary contents differ (%d -> %d bytes)\n", len(old.Data), len(next.Data))
		} else if !bytes.Equal(old.Data, next.Data) {
			out.WriteString(textDiff(old.Data, next.Data))
		}
		change.Diff = out.String()
		changes = append(changes, change)
	}
	return changes
}

func fileDigest(file File) string {
	return digest(append([]byte(fmt.Sprintf("%04o\x00%s\x00", file.Mode, file.Link)), file.Data...))
}

func isText(data []byte) bool { return utf8.Valid(data) && !bytes.ContainsRune(data, '\x00') }

func textDiff(before, after []byte) string {
	const limit = 128 << 10
	if len(before)+len(after) > limit {
		return fmt.Sprintf("Text diff omitted: %d bytes exceeds display limit of %d bytes\n", len(before)+len(after), limit)
	}
	a, b := lines(before), lines(after)
	// Trim a shared prefix/suffix, keeping three context lines at each end.
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	start := prefix - 3
	if start < 0 {
		start = 0
	}
	endA, endB := len(a)-suffix+3, len(b)-suffix+3
	if endA > len(a) {
		endA = len(a)
	}
	if endB > len(b) {
		endB = len(b)
	}
	var out strings.Builder
	lineA, lineB := start+1, start+1
	if endA-start == 0 {
		lineA = start
	}
	if endB-start == 0 {
		lineB = start
	}
	fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", lineA, endA-start, lineB, endB-start)
	write := func(mark byte, line string) {
		out.WriteByte(mark)
		out.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			out.WriteString("\n\\ No newline at end of file\n")
		}
	}
	for i := start; i < prefix; i++ {
		write(' ', a[i])
	}
	for i := prefix; i < len(a)-suffix; i++ {
		write('-', a[i])
	}
	for i := prefix; i < len(b)-suffix; i++ {
		write('+', b[i])
	}
	for i := len(a) - suffix; i < endA; i++ {
		write(' ', a[i])
	}
	return out.String()
}

func lines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	result := strings.SplitAfter(string(data), "\n")
	if result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}
	return result
}
