// Package toolcall - gbnf_test.go
package toolcall

import (
	"encoding/json"
	"strings"
	"testing"
)

// mustGBNF 转换并在出错时 fail。
func mustGBNF(t *testing.T, schema string) string {
	t.Helper()
	out, err := SchemaToGBNF(json.RawMessage(schema))
	if err != nil {
		t.Fatalf("SchemaToGBNF(%s): %v", schema, err)
	}
	if !strings.HasPrefix(out, "root ::= ") {
		t.Fatalf("GBNF 应以 'root ::= ' 开头:\n%s", out)
	}
	return out
}

func TestGBNF_Empty(t *testing.T) {
	// 空 schema → 任意 JSON 值
	out := mustGBNF(t, ``)
	if !strings.Contains(out, "value") {
		t.Errorf("空 schema 应降级 value:\n%s", out)
	}
}

func TestGBNF_String(t *testing.T) {
	out := mustGBNF(t, `{"type":"string"}`)
	if !strings.Contains(out, "root ::= string") {
		t.Errorf("string schema:\n%s", out)
	}
	// 必须定义 string / char 原语
	if !strings.Contains(out, "string ::=") || !strings.Contains(out, "char ::=") {
		t.Errorf("缺 string/char 原语:\n%s", out)
	}
}

func TestGBNF_Integer(t *testing.T) {
	out := mustGBNF(t, `{"type":"integer"}`)
	if !strings.Contains(out, "root ::= integer") {
		t.Errorf("integer schema:\n%s", out)
	}
	if !strings.Contains(out, "integer ::=") {
		t.Errorf("缺 integer 原语:\n%s", out)
	}
}

func TestGBNF_Boolean(t *testing.T) {
	out := mustGBNF(t, `{"type":"boolean"}`)
	if !strings.Contains(out, `"true" | "false"`) {
		t.Errorf("boolean 应为 true|false:\n%s", out)
	}
}

func TestGBNF_Enum(t *testing.T) {
	out := mustGBNF(t, `{"type":"string","enum":["allow","deny","ask"]}`)
	// enum 值是 JSON 字符串，GBNF 字面量含转义引号：\"allow\"
	if !strings.Contains(out, `\"allow\"`) || !strings.Contains(out, `\"deny\"`) || !strings.Contains(out, `\"ask\"`) {
		t.Errorf("enum 字面量缺失:\n%s", out)
	}
	if !strings.Contains(out, "|") {
		t.Errorf("enum 应有交替 |:\n%s", out)
	}
}

func TestGBNF_SimpleObject(t *testing.T) {
	schema := `{
		"type":"object",
		"properties":{
			"path":{"type":"string"},
			"limit":{"type":"integer"}
		},
		"required":["path"]
	}`
	out := mustGBNF(t, schema)
	// 必含两个属性名字面量
	if !strings.Contains(out, `"path"`) {
		t.Errorf("缺 path 属性:\n%s", out)
	}
	if !strings.Contains(out, `"limit"`) {
		t.Errorf("缺 limit 属性:\n%s", out)
	}
	// 必有花括号字面量
	if !strings.Contains(out, `"{"`) || !strings.Contains(out, `"}"`) {
		t.Errorf("缺对象花括号:\n%s", out)
	}
	// limit 是可选的，应被 ()? 包裹
	if !strings.Contains(out, ")?") {
		t.Errorf("可选属性应有 ()?:\n%s", out)
	}
}

func TestGBNF_ObjectAllRequired(t *testing.T) {
	schema := `{
		"type":"object",
		"properties":{"a":{"type":"string"},"b":{"type":"string"}},
		"required":["a","b"]
	}`
	out := mustGBNF(t, schema)
	// 两个 required 用逗号连接
	if !strings.Contains(out, `","`) {
		t.Errorf("required 成员应有逗号:\n%s", out)
	}
}

func TestGBNF_ObjectNoProperties(t *testing.T) {
	// 无 properties 的 object 退化为通用 object
	out := mustGBNF(t, `{"type":"object"}`)
	if !strings.Contains(out, "root ::= object") {
		t.Errorf("空 object 应退化通用 object:\n%s", out)
	}
}

func TestGBNF_Array(t *testing.T) {
	out := mustGBNF(t, `{"type":"array","items":{"type":"string"}}`)
	if !strings.Contains(out, `"["`) || !strings.Contains(out, `"]"`) {
		t.Errorf("缺数组方括号:\n%s", out)
	}
	// items 是 string，应引用 string 规则
	if !strings.Contains(out, "string") {
		t.Errorf("数组元素应引用 string:\n%s", out)
	}
}

func TestGBNF_ArrayMinItems(t *testing.T) {
	out := mustGBNF(t, `{"type":"array","items":{"type":"integer"},"minItems":1}`)
	// minItems>=1：至少一个元素，不应有最外层 ()?
	// 用 ( ws "," ws ... )* 表示后续可重复
	if !strings.Contains(out, ")*") {
		t.Errorf("数组应有重复 ()*:\n%s", out)
	}
}

func TestGBNF_NestedObject(t *testing.T) {
	schema := `{
		"type":"object",
		"properties":{
			"filter":{
				"type":"object",
				"properties":{"name":{"type":"string"}},
				"required":["name"]
			}
		},
		"required":["filter"]
	}`
	out := mustGBNF(t, schema)
	// 应有两层 object 规则（嵌套）
	if strings.Count(out, "object-") < 2 {
		t.Errorf("嵌套对象应生成多个 object 规则:\n%s", out)
	}
	if !strings.Contains(out, `"filter"`) || !strings.Contains(out, `"name"`) {
		t.Errorf("嵌套属性名缺失:\n%s", out)
	}
}

func TestGBNF_ArrayOfObjects(t *testing.T) {
	schema := `{
		"type":"array",
		"items":{
			"type":"object",
			"properties":{"id":{"type":"integer"}},
			"required":["id"]
		}
	}`
	out := mustGBNF(t, schema)
	if !strings.Contains(out, "array-") {
		t.Errorf("应生成 array 规则:\n%s", out)
	}
	if !strings.Contains(out, `"id"`) {
		t.Errorf("数组元素对象属性缺失:\n%s", out)
	}
}

func TestGBNF_BrokenSchemaDegrades(t *testing.T) {
	// 坏 schema 不应 error，降级为任意值
	out, err := SchemaToGBNF(json.RawMessage(`{not json`))
	if err != nil {
		t.Fatalf("坏 schema 不应返错误: %v", err)
	}
	if !strings.Contains(out, "value") {
		t.Errorf("坏 schema 应降级 value:\n%s", out)
	}
}

func TestGBNF_UnknownTypeDegrades(t *testing.T) {
	out := mustGBNF(t, `{"type":"weird"}`)
	if !strings.Contains(out, "root ::= value") {
		t.Errorf("未知 type 应降级 value:\n%s", out)
	}
}

func TestGBNF_AllPrimitivesDefined(t *testing.T) {
	// 任意对象 schema 都应包含全套原语定义（保证 GBNF 自洽可解析）
	out := mustGBNF(t, `{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`)
	for _, prim := range []string{"value ::=", "string ::=", "number ::=", "integer ::=", "boolean ::=", "null ::=", "ws ::=", "char ::=", "hex ::="} {
		if !strings.Contains(out, prim) {
			t.Errorf("缺原语定义 %q:\n%s", prim, out)
		}
	}
}

func TestGBNF_StableOutput(t *testing.T) {
	// 同一 schema 多次转换输出应完全一致（属性排序保证可测）
	schema := `{"type":"object","properties":{"z":{"type":"string"},"a":{"type":"integer"}},"required":["z","a"]}`
	out1 := mustGBNF(t, schema)
	out2 := mustGBNF(t, schema)
	if out1 != out2 {
		t.Errorf("输出应稳定一致:\n--- 1 ---\n%s\n--- 2 ---\n%s", out1, out2)
	}
}

func TestGBNFLiteral_Escaping(t *testing.T) {
	got := gbnfLiteral(`a"b\c`)
	if got != `"a\"b\\c"` {
		t.Errorf("转义错误: %q", got)
	}
}

func TestCompactJSON(t *testing.T) {
	got, err := compactJSON(json.RawMessage(`{ "a" : 1 ,  "b":2 }`))
	if err != nil {
		t.Fatal(err)
	}
	// 紧凑后无多余空白（顺序由 encoding/json 决定，这里只校验无空格）
	if strings.Contains(got, "  ") {
		t.Errorf("应无多余空白: %q", got)
	}
}

// TestGBNF_RealToolSchema 用真实工具的 schema 跑一遍，确保不 panic、产出合理。
func TestGBNF_RealToolSchema(t *testing.T) {
	// 模拟 read 工具的 schema
	schema := `{
		"type":"object",
		"properties":{
			"path":{"type":"string","description":"file path"},
			"offset":{"type":"integer","minimum":0},
			"limit":{"type":"integer","minimum":0}
		},
		"required":["path"],
		"additionalProperties":false
	}`
	out := mustGBNF(t, schema)
	if !strings.Contains(out, `"path"`) {
		t.Errorf("read schema 缺 path:\n%s", out)
	}
	// offset/limit 可选
	if strings.Count(out, ")?") < 2 {
		t.Errorf("应有 2 个可选属性:\n%s", out)
	}
}
