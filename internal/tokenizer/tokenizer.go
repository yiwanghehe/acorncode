// Package tokenizer 提供纯 stdlib 的 token 数启发式估算。
//
// 设计动机：精确 tokenize 需要模型词表（tiktoken/SentencePiece），会引入第三方
// 依赖，违背项目「3 依赖」约束（见 AGENTS.md §1.4）。本包用启发式逼近主流 BPE
// 分词器（GPT/Qwen/Llama）的统计规律，把误差从 `len/4` 的 ±50% 收敛到约 ±15%，
// 0 第三方依赖、确定性、可测。
//
// 估算规则（经验值，对齐 cl100k / Qwen BPE 的平均行为）：
//   - ASCII 单词：约每 4 个字符 1 token，但至少 1 token（短词如 "a"/"is" = 1）
//   - 数字串：约每 3 位 1 token（BPE 常把数字切成 2~3 位一段）
//   - 标点 / 符号：各算 1 token
//   - CJK（中日韩）字符：每字约 1 token（中文在多数 BPE 里 1~2 token/字，取保守下界）
//   - 其它 Unicode（emoji 等）：每个 rune 约 1~2 token，统一按 2 估
package tokenizer

import (
	"unicode"
)

// 经验常数（集中放此处，便于调参与说明）
const (
	charsPerWordToken = 4 // ASCII 单词：每 4 字符 ≈ 1 token
	digitsPerToken    = 3 // 数字串：每 3 位 ≈ 1 token
	tokensPerCJKChar  = 1 // CJK：每字 ≈ 1 token
	tokensPerOther    = 2 // 其它 rune（emoji 等）≈ 2 token
)

// Count 估算字符串的 token 数。空串返回 0。
//
// 实现：单次遍历 rune，按字符类别累加。把连续的 ASCII 字母聚成「单词」、
// 连续数字聚成「数字串」分别折算，标点/CJK/其它各自计数。
func Count(s string) int {
	if s == "" {
		return 0
	}

	total := 0
	wordLen := 0  // 当前累积的 ASCII 字母数
	digitLen := 0 // 当前累积的数字位数

	// flush 把当前累积的单词 / 数字串折算成 token 并清零
	flush := func() {
		if wordLen > 0 {
			total += wordTokens(wordLen)
			wordLen = 0
		}
		if digitLen > 0 {
			total += ceilDiv(digitLen, digitsPerToken)
			digitLen = 0
		}
	}

	for _, r := range s {
		switch {
		case isASCIILetter(r):
			// 数字与字母相邻时先 flush 数字（如 "abc123" 拆成 word + number）
			if digitLen > 0 {
				total += ceilDiv(digitLen, digitsPerToken)
				digitLen = 0
			}
			wordLen++
		case unicode.IsDigit(r) && r < 128:
			if wordLen > 0 {
				total += wordTokens(wordLen)
				wordLen = 0
			}
			digitLen++
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			// 空白：结束当前 word/number，自身不计 token（BPE 多把前导空格并入下一词）
			flush()
		case isCJK(r):
			flush()
			total += tokensPerCJKChar
		case r < 128:
			// ASCII 标点 / 符号：各 1 token
			flush()
			total++
		default:
			// 其它 Unicode（emoji、生僻符号）
			flush()
			total += tokensPerOther
		}
	}
	flush()
	return total
}

// wordTokens 把一个 ASCII 单词（wordLen 个字母）折算成 token 数，至少 1。
func wordTokens(wordLen int) int {
	n := ceilDiv(wordLen, charsPerWordToken)
	if n < 1 {
		n = 1
	}
	return n
}

// isASCIILetter 判断是否 ASCII 字母
func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// isCJK 判断是否中日韩统一表意文字及常见假名 / 谚文区间
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一表意文字
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK 扩展 A
		return true
	case r >= 0x3040 && r <= 0x30FF: // 平假名 + 片假名
		return true
	case r >= 0xAC00 && r <= 0xD7A3: // 谚文音节
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK 兼容表意文字
		return true
	}
	return false
}

// ceilDiv 向上取整除法（a>0, b>0）
func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}
