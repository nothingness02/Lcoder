package skills

import (
	"strings"
)

// MatchScore represents the relevance of a skill to a user prompt.
type MatchScore struct {
	Skill   SkillMeta
	Score   float64
	Reasons []string
}

// AutoDetect selects the best matching skill from a catalog for a user prompt.
// It uses simple keyword heuristics. A value of 0 means no confident match.
func AutoDetect(prompt string, catalog []SkillMeta) (MatchScore, bool) {
	lower := strings.ToLower(prompt)
	var best MatchScore

	for _, skill := range catalog {
		score, reasons := scoreSkill(lower, skill)
		if score > best.Score {
			best = MatchScore{Skill: skill, Score: score, Reasons: reasons}
		}
	}

	// Require a minimum confidence before activating a skill automatically.
	if best.Score < 0.3 {
		return MatchScore{}, false
	}
	return best, true
}

func scoreSkill(prompt string, skill SkillMeta) (float64, []string) {
	var score float64
	var reasons []string

	// Match against skill name.
	name := strings.ToLower(skill.Name)
	if strings.Contains(prompt, name) {
		score += 0.5
		reasons = append(reasons, "name match")
	}

	// Match against description.
	desc := strings.ToLower(skill.Description)
	for _, word := range tokenize(desc) {
		if len(word) > 3 && strings.Contains(prompt, word) {
			score += 0.2
			reasons = append(reasons, "description keyword")
			break
		}
	}

	// Match against keywords.
	for _, kw := range skill.Keywords {
		kwLower := strings.ToLower(kw)
		for _, word := range tokenize(kwLower) {
			if len(word) > 3 && strings.Contains(prompt, word) {
				score += 0.25
				reasons = append(reasons, "keyword match")
				break
			}
		}
	}

	return score, reasons
}

func tokenize(text string) []string {
	replacer := strings.NewReplacer(
		",", " ",
		".", " ",
		":", " ",
		";", " ",
		"(", " ",
		")", " ",
		"[", " ",
		"]", " ",
		"{", " ",
		"}", " ",
	)
	text = replacer.Replace(text)
	var tokens []string
	for _, p := range strings.Fields(text) {
		tokens = append(tokens, strings.ToLower(p))
	}
	return tokens
}
