package toolmention

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/chaoss/disclosure/detection"
)

func getToolPatterns() []toolPattern {
	var toolPatterns []toolPattern
	names := append([]string(nil), detection.SupportedToolsInMentions...)
	for _, name := range names {
		toolPatterns = append(toolPatterns, toolPattern{
			name:    name,
			pattern: regexp.MustCompile(toolMentionPattern(name)),
		})
	}
	return toolPatterns
}

func toolMentionPattern(name string) string {
	const separator = `[\s_-]+`
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == ' ' || r == '-'
	})
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	body := strings.Join(parts, separator)
	if len(parts) == 0 {
		body = regexp.QuoteMeta(name)
	}
	pattern := `(?i)\b` + body
	last, _ := utf8.DecodeLastRuneInString(name)
	if unicode.IsLetter(last) || unicode.IsDigit(last) || last == '_' {
		return pattern + `\b`
	}
	return pattern + `(?:$|[^A-Za-z0-9_])`
}

func newCheckboxRegex(label string) *regexp.Regexp {
	return regexp.MustCompile(
		`(?im)^[ \t]*[*+-]?[ \t]*\[[ \t]*(x)?[ \t]*\][ \t]+` + regexp.QuoteMeta(label) + `[ \t]*\r?$`,
	)
}

func stripComments(text string) string {
	re := regexp.MustCompile(detection.HtmlCommentPattern)
	return re.ReplaceAllString(text, "")
}

func (d *Detector) matchTools(text string) []toolMatch {
	patterns := d.patterns
	if patterns == nil {
		patterns = toolPatterns
	}
	matches := make([]toolMatch, 0, len(patterns))
	for _, tp := range patterns {
		for _, loc := range tp.pattern.FindAllStringIndex(text, -1) {
			matches = append(matches, toolMatch{
				start: loc[0],
				end:   loc[1],
				name:  tp.name,
			})
		}
	}

	// Give priority to longer matches when there's an overlap.
	// e.g. Claude Code would be preferred over Claude
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].start != matches[j].start {
			return matches[i].start < matches[j].start
		}
		return matches[i].end-matches[i].start > matches[j].end-matches[j].start
	})

	toolMatches := make([]toolMatch, 0, len(matches))
	seen := make(map[string]struct{}, len(patterns))
	lastEnd := -1
	for _, match := range matches {
		if match.start < lastEnd {
			continue
		}
		if _, ok := seen[match.name]; ok {
			continue
		}
		toolMatches = append(toolMatches, match)
		seen[match.name] = struct{}{}
		lastEnd = match.end
	}

	return toolMatches
}

type toolPattern struct {
	name    string
	pattern *regexp.Regexp
}

type toolMatch struct {
	start int
	end   int
	name  string
}

type Detector struct {
	ConfidenceLevels         map[detection.Confidence]float64
	CheckboxDetectionEnabled bool
	CheckboxAIUsedRegex      *regexp.Regexp
	CheckboxAINotUsedRegex   *regexp.Regexp
	initOnce                 sync.Once
	patterns                 []toolPattern
}

var toolPatterns []toolPattern

func init() {
	toolPatterns = getToolPatterns()
}

func (d *Detector) Name() string { return "toolmention" }

func (d *Detector) GetConfidenceLevels() map[detection.Confidence]float64 { return d.ConfidenceLevels }

func (d *Detector) appendFinding(findings *[]detection.Finding, toolName string, score float64, detail string) {
	*findings = append(*findings, detection.Finding{
		Detector:   d.Name(),
		Tool:       toolName,
		Score:      score,
		Confidence: detection.ScoreToConfidence(d.ConfidenceLevels, score),
		Detail:     detail,
	})
}

func (d *Detector) SetConfidenceLevels(confidenceLevels map[detection.Confidence]float64) {
	d.ConfidenceLevels = confidenceLevels
}

// SetCustomTools supplements the built-in names for this detector only.
// Call it before Detect; repeated calls replace the previous custom names.
func (d *Detector) SetCustomTools(names []string) error {
	if len(names) == 0 {
		d.patterns = nil
		return nil
	}
	patterns := append([]toolPattern(nil), toolPatterns...)
	seen := make(map[string]bool, len(patterns)+len(names))
	for _, tp := range patterns {
		seen[strings.ToLower(tp.name)] = true
	}
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("custom_tools[%d] must be a non-empty name", i)
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		pattern, err := regexp.Compile(toolMentionPattern(name))
		if err != nil {
			return fmt.Errorf("custom_tools[%d]: %w", i, err)
		}
		patterns = append(patterns, toolPattern{name: name, pattern: pattern})
		seen[key] = true
	}
	d.patterns = patterns
	return nil
}

// SetCheckboxConfig configures the checkbox labels.
// Call this before the first Detect call to use custom labels,
// otherwise Detect initializes the default labels.
func (d *Detector) SetCheckboxConfig(enableCheckboxDetection bool, aiUsedLabel string, aiNotUsedLabel string) {
	if aiUsedLabel == "" {
		aiUsedLabel = detection.DefaultCheckboxAIUsedLabel
	}
	if aiNotUsedLabel == "" {
		aiNotUsedLabel = detection.DefaultCheckboxAINotUsedLabel
	}
	d.CheckboxDetectionEnabled = enableCheckboxDetection
	d.CheckboxAIUsedRegex = newCheckboxRegex(strings.TrimSpace(aiUsedLabel))
	d.CheckboxAINotUsedRegex = newCheckboxRegex(strings.TrimSpace(aiNotUsedLabel))
}

func (d *Detector) Detect(input detection.Input) []detection.Finding {
	text, err := input.GetTextWithCommitMessage()
	if err != nil {
		return []detection.Finding{}
	}

	// Let the checkbox aware detection run instead of text detection
	if d.CheckboxDetectionEnabled {
		return d.checkboxAwareDetect(text)
	}

	toolMatches := d.matchTools(text)
	findings := make([]detection.Finding, 0, len(toolMatches))
	if len(toolMatches) > 0 {
		score := detection.ToolMentionBaseScore
		for _, match := range toolMatches {
			d.appendFinding(&findings, match.name, score, "text mentions "+match.name)
		}
		return findings
	}
	return findings
}

func (d *Detector) checkboxAwareDetect(inputText string) []detection.Finding {
	inputText = stripComments(inputText)
	toolMatches := d.matchTools(inputText)

	var aiUsedCbTicked, aiNotUsedCbTicked bool

	// check if ai use checkbox found
	aiUsedMatches := d.CheckboxAIUsedRegex.FindStringSubmatch(inputText)
	if len(aiUsedMatches) > 0 {
		// if found, then we check if it's ticked
		aiUsedCbTicked = strings.TrimSpace(strings.ToLower(aiUsedMatches[1])) == "x"
	}

	// check if no ai use checkbox found
	aiNotUsedMatches := d.CheckboxAINotUsedRegex.FindStringSubmatch(inputText)
	if len(aiNotUsedMatches) > 0 {
		// if found then we check if it's ticked
		aiNotUsedCbTicked = strings.TrimSpace(strings.ToLower(aiNotUsedMatches[1])) == "x"
	}
	isAIUseConfirmed := aiUsedCbTicked && !aiNotUsedCbTicked

	detail := ""
	if isAIUseConfirmed {
		detail = "checkbox confirms AI was used and "
	}

	findings := make([]detection.Finding, 0, len(toolMatches))

	// tool mentions and checkbox
	if len(toolMatches) > 0 {
		score := detection.ToolMentionBaseScore
		if isAIUseConfirmed {
			score += detection.CheckboxAIUsedBaseScore
		}
		for _, match := range toolMatches {
			d.appendFinding(&findings, match.name, score, detail+"text mentions "+match.name)
		}
		return findings
	}

	// checkbox only
	if isAIUseConfirmed {
		score := detection.CheckboxAIUsedBaseScore
		d.appendFinding(&findings, "", score, detail+"text does not mention any specific tool")
	}

	return findings
}
