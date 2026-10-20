package tui

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var fuzzySwapPattern = regexp.MustCompile(`^([a-z]+)([0-9]+)$|^([0-9]+)([a-z]+)$`)

// fuzzyCommands 按子序列评分稳定排序，支持空白与斜杠分词
func fuzzyCommands(items []slashCommand, query string, bare bool) []slashCommand {
	tokens := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return unicode.IsSpace(r) || r == '/' })
	type candidate struct {
		command slashCommand
		score   int
	}
	var matches []candidate
	for _, item := range items {
		name := item.name
		if bare {
			name = strings.TrimPrefix(name, "skill:")
		}
		score, matched := 0, true
		for _, token := range tokens {
			value, ok := fuzzyTokenScore(token, strings.ToLower(name))
			if !ok {
				matched = false
				break
			}
			score += value
		}
		if matched {
			matches = append(matches, candidate{command: item, score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score < matches[j].score })
	result := make([]slashCommand, len(matches))
	for i, match := range matches {
		result[i] = match.command
	}
	return result
}

// fuzzyTokenScore 保留字母数字顺序交换回退，优先使用原查询
func fuzzyTokenScore(query, text string) (int, bool) {
	if score, ok := fuzzySubsequenceScore(query, text); ok {
		return score, true
	}
	match := fuzzySwapPattern.FindStringSubmatch(query)
	if match == nil {
		return 0, false
	}
	swapped := match[2] + match[1] + match[4] + match[3]
	score, ok := fuzzySubsequenceScore(swapped, text)
	return score + 50, ok
}

// fuzzySubsequenceScore 使用连续匹配、词边界和间距权重，分值放大十倍以使用整数
func fuzzySubsequenceScore(query, text string) (int, bool) {
	last, consecutive, score := -1, 0, 0
	for i := 0; i < len(query); i++ {
		offset := strings.IndexByte(text[last+1:], query[i])
		if offset == -1 {
			return 0, false
		}
		index := last + 1 + offset
		if index == last+1 {
			consecutive++
			score -= consecutive * 50
		} else {
			consecutive = 0
			if last >= 0 {
				score += (index - last - 1) * 20
			}
		}
		if index == 0 || strings.ContainsRune(" \t\r\n-_./:", rune(text[index-1])) {
			score -= 100
		}
		score += index
		last = index
	}
	if query == text {
		score -= 1000
	}
	return score, true
}
