package validation

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"actionlint.kjanat.dev/analyzer"
	"actionlint.kjanat.dev/diagnostic"
)

func validRange(content string, start, end diagnostic.Position) bool {
	lines := strings.Split(content, "\n")
	valid := func(p diagnostic.Position) bool {
		return p.Line > 0 && p.Line <= len(lines) && p.Column > 0 && p.Column <= utf8.RuneCountInString(lines[p.Line-1])+1
	}
	return valid(start) && valid(end) && (start.Line < end.Line || start.Line == end.Line && start.Column <= end.Column)
}

func scriptFindings(findings []analyzer.ScriptDiagnostic, request analyzer.ScriptRequest) error {
	if len(findings) > 4096 {
		return errors.New("too many sample diagnostics")
	}
	for _, d := range findings {
		if d.Filename != request.Filename || d.Message == "" || !boundedText(d.Message, 16384) || !boundedText(d.Code, 128) {
			return errors.New("invalid script diagnostic identity or message")
		}
		if !validRange(request.Source, diagnostic.Position{Line: d.Location.Row, Column: d.Location.Column},
			diagnostic.Position{Line: d.EndLocation.Row, Column: d.EndLocation.Column}) {
			return errors.New("script diagnostic outside sample source")
		}
	}
	return nil
}

func componentFindings(findings []diagnostic.Diagnostic, input analyzer.Input, rules []string) error {
	if len(findings) > 4096 {
		return errors.New("too many sample diagnostics")
	}
	validPath := func(path string) bool { return path == "" || path == input.Path }
	for _, d := range findings {
		if !validPath(d.Path) || d.Rule != "" && !slices.Contains(rules, d.Rule) || d.Message == "" || !boundedText(d.Message, 16384) || !boundedText(d.Code, 128) {
			return errors.New("invalid component diagnostic identity or message")
		}
		if !validRange(input.Content, d.Start, d.End) {
			return errors.New("component diagnostic outside sample source")
		}
		if d.Reference != nil && (d.Reference.Document != input.Reference.Document || d.Reference.Revision != input.Reference.Revision) {
			return errors.New("component diagnostic references a different source revision")
		}
		if len(d.Related) > 128 || len(d.Fixes) > 128 {
			return errors.New("too many related locations or fixes")
		}
		for _, related := range d.Related {
			if !validPath(related.Path) || !validRange(input.Content, related.Start, related.End) {
				return errors.New("related location outside sample source")
			}
		}
		for _, fix := range d.Fixes {
			if len(fix.Edits) > 128 || len(fix.Revisions) > 1 {
				return errors.New("too many fix edits or revisions")
			}
			for path, revision := range fix.Revisions {
				if path != input.Path || revision != input.Reference.Revision {
					return errors.New("fix references a different source revision")
				}
			}
			for _, edit := range fix.Edits {
				if !validPath(edit.Path) || !validRange(input.Content, edit.Start, edit.End) || !boundedText(edit.Replacement, 65536) {
					return errors.New("invalid edit or edit outside sample source")
				}
			}
		}
	}
	return nil
}
