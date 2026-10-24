package validation

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"actionlint.kjanat.dev/v2/analyzer"
	"actionlint.kjanat.dev/v2/diagnostic"
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
		return errors.New("the sample produced more than 4096 diagnostics")
	}
	for _, d := range findings {
		if d.Filename != request.Filename || d.Message == "" || !boundedText(d.Message, 16384) || !boundedText(d.Code, 128) {
			return errors.New("the script diagnostic has an invalid filename, code, or message")
		}
		if !validRange(request.Source, diagnostic.Position{Line: d.Location.Row, Column: d.Location.Column},
			diagnostic.Position{Line: d.EndLocation.Row, Column: d.EndLocation.Column}) {
			return errors.New("the script diagnostic points outside the sample source")
		}
	}
	return nil
}

func componentFindings(findings []diagnostic.Diagnostic, input analyzer.Input, rules []string) error {
	if len(findings) > 4096 {
		return errors.New("the sample produced more than 4096 diagnostics")
	}
	validPath := func(path string) bool { return path == "" || path == input.Path }
	for _, d := range findings {
		if !validPath(d.Path) || d.Rule != "" && !slices.Contains(rules, d.Rule) || d.Message == "" || !boundedText(d.Message, 16384) || !boundedText(d.Code, 128) {
			return errors.New("the component diagnostic has an invalid path, rule, code, or message")
		}
		if !validRange(input.Content, d.Start, d.End) {
			return errors.New("the component diagnostic points outside the sample source")
		}
		if d.Reference != nil && (d.Reference.Document != input.Reference.Document || d.Reference.Revision != input.Reference.Revision) {
			return errors.New("component diagnostic references a different source revision")
		}
		if len(d.Related) > 128 || len(d.Fixes) > 128 {
			return errors.New("the diagnostic contains more than 128 related locations or fixes")
		}
		for _, related := range d.Related {
			if !validPath(related.Path) || !validRange(input.Content, related.Start, related.End) {
				return errors.New("the related location points outside the sample source")
			}
		}
		for _, fix := range d.Fixes {
			if len(fix.Edits) > 128 || len(fix.Revisions) > 1 {
				return errors.New("the fix contains more than 128 edits or references more than one revision")
			}
			for path, revision := range fix.Revisions {
				if path != input.Path || revision != input.Reference.Revision {
					return errors.New("fix references a different source revision")
				}
			}
			for _, edit := range fix.Edits {
				if !validPath(edit.Path) || !validRange(input.Content, edit.Start, edit.End) || !boundedText(edit.Replacement, 65536) {
					return errors.New("the edit has an invalid replacement or points outside the sample source")
				}
			}
		}
	}
	return nil
}
