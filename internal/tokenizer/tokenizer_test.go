package tokenizer

import "testing"

func TestCount_Empty(t *testing.T) {
	if got := Count(""); got != 0 {
		t.Errorf("空串 = %d, 期望 0", got)
	}
}

func TestCount_SingleWord(t *testing.T) {
	// 短词应为 1 token
	for _, w := range []string{"a", "is", "the", "code"} {
		if got := Count(w); got != 1 {
			t.Errorf("Count(%q) = %d, 期望 1", w, got)
		}
	}
}

func TestCount_LongWord(t *testing.T) {
	// "tokenization" 12 字母 → ceil(12/4) = 3
	if got := Count("tokenization"); got != 3 {
		t.Errorf("Count(tokenization) = %d, 期望 3", got)
	}
}

func TestCount_Sentence(t *testing.T) {
	// "I have a cat." → I(1) have(1) a(1) cat(1) .(1) = 5
	if got := Count("I have a cat."); got != 5 {
		t.Errorf("Count = %d, 期望 5", got)
	}
}

func TestCount_Digits(t *testing.T) {
	// "12345" 5 位 → ceil(5/3) = 2
	if got := Count("12345"); got != 2 {
		t.Errorf("Count(12345) = %d, 期望 2", got)
	}
	// "1" → 1
	if got := Count("1"); got != 1 {
		t.Errorf("Count(1) = %d, 期望 1", got)
	}
}

func TestCount_WordDigitMix(t *testing.T) {
	// "abc123" → abc(1) + 123(1) = 2
	if got := Count("abc123"); got != 2 {
		t.Errorf("Count(abc123) = %d, 期望 2", got)
	}
}

func TestCount_CJK(t *testing.T) {
	// 中文每字 1 token：「你好世界」= 4
	if got := Count("你好世界"); got != 4 {
		t.Errorf("Count(你好世界) = %d, 期望 4", got)
	}
}

func TestCount_CJKMixed(t *testing.T) {
	// "用 go build 编译" → 用(1) go(1) build(2->ceil(5/4)) 编(1) 译(1)
	// build=5 字母 → ceil(5/4)=2；空格不计
	got := Count("用 go build 编译")
	want := 1 + 1 + 2 + 1 + 1
	if got != want {
		t.Errorf("Count = %d, 期望 %d", got, want)
	}
}

func TestCount_Punctuation(t *testing.T) {
	// "a, b!" → a(1) ,(1) b(1) !(1) = 4（空格不计）
	if got := Count("a, b!"); got != 4 {
		t.Errorf("Count(a, b!) = %d, 期望 4", got)
	}
}

func TestCount_WhitespaceNotCounted(t *testing.T) {
	// 纯空白 → 0
	if got := Count("   \n\t "); got != 0 {
		t.Errorf("纯空白 = %d, 期望 0", got)
	}
}

func TestCount_Emoji(t *testing.T) {
	// emoji 按 2 token；"hi 😀" → hi(1) + 😀(2) = 3
	if got := Count("hi 😀"); got != 3 {
		t.Errorf("Count(hi 😀) = %d, 期望 3", got)
	}
}

// TestCount_BetterThanCharDiv4 验证对中文场景比 len/4 更准。
// 中文 UTF-8 每字 3 字节，len/4 会严重低估。
func TestCount_BetterThanCharDiv4(t *testing.T) {
	s := "这是一段中文测试文本用来验证分词估算" // 18 字
	got := Count(s)
	if got != 18 {
		t.Errorf("Count = %d, 期望 18", got)
	}
	// 旧实现 len(s)/4：18 字 × 3 字节 = 54 字节 / 4 = 13，明显低估
	old := len(s) / 4
	if old >= got {
		t.Errorf("新估算(%d)应高于旧 len/4 估算(%d)（中文旧法低估）", got, old)
	}
}
