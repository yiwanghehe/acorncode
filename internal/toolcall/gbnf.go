// Package toolcall - gbnf.go
//
// Schema→GBNF 转换器（v1.3）。把 JSON Schema 转成 GBNF（GGML BNF）语法，
// 让 llama.cpp / Ollama 在解码阶段「强制」输出符合 schema 的 JSON，
// 而非事后验证（v1.0.6 的做法）。这是「约束生成」对「校验」的升级。
//
// GBNF 参考：https://github.com/ggerganov/llama.cpp/blob/master/grammars/README.md
//
// **范围（v1.3）**：支持 JSON Schema 的常用子集——
//   - 类型：object / array / string / number / integer / boolean / null
//   - object：properties / required / additionalProperties(false)
//   - array：items / minItems(0|1)
//   - string：enum（字符串字面量）
//   - 组合：枚举退化为字面量交替
//
// **不支持（信任模型 + 事后校验兜底）**：$ref / oneOf / allOf / pattern / format /
// 数值范围（minimum/maximum）/ 复杂的 additionalProperties schema。
// 遇到不支持的结构，降级为「任意 JSON 值」规则，保证不报错、不卡生成。
//
// 0 新依赖：纯 stdlib（encoding/json + strings + fmt + sort）。
package toolcall

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// jsonSchema 是 JSON Schema 的部分反序列化结构（只取 GBNF 需要的字段）。
type jsonSchema struct {
	Type                 string                     `json:"type"`
	Properties           map[string]json.RawMessage `json:"properties"`
	Required             []string                   `json:"required"`
	Items                json.RawMessage            `json:"items"`
	Enum                 []json.RawMessage          `json:"enum"`
	AdditionalProperties *bool                      `json:"additionalProperties"`
	MinItems             *int                       `json:"minItems"`
}

// gbnfBuilder 累积生成的命名规则，去重并按需引用。
type gbnfBuilder struct {
	rules    map[string]string // 规则名 → 规则体
	order    []string          // 规则定义顺序（保证输出稳定）
	seq      int               // 匿名规则自增计数
	maxDepth int               // 递归深度上限，防恶意/超深 schema
}

// SchemaToGBNF 把单个 JSON Schema 转成完整的 GBNF 语法字符串。
// 根规则名固定为 "root"。schema 为空时返回「任意 JSON 值」语法。
func SchemaToGBNF(schema json.RawMessage) (string, error) {
	b := &gbnfBuilder{
		rules:    make(map[string]string),
		maxDepth: 32,
	}
	rootRef, err := b.convert(schema, 0)
	if err != nil {
		return "", err
	}
	// root 规则指向转换结果（外加可选首尾空白）
	b.define("root", rootRef)
	b.ensurePrimitives()
	return b.render(), nil
}

// convert 把一个（子）schema 转成「引用名」，并在 builder 里登记规则。
// 返回的字符串是可直接放进其他规则右侧的引用（规则名或字面量组）。
func (b *gbnfBuilder) convert(raw json.RawMessage, depth int) (string, error) {
	if depth > b.maxDepth {
		return "value", nil // 超深：降级任意值
	}
	if len(raw) == 0 {
		return "value", nil
	}
	var s jsonSchema
	if err := json.Unmarshal(raw, &s); err != nil {
		// schema 坏：降级任意值，不报错（约定：不阻断生成）
		return "value", nil
	}

	// enum 优先：退化为字面量交替
	if len(s.Enum) > 0 {
		return b.convertEnum(s.Enum)
	}

	switch s.Type {
	case "object":
		return b.convertObject(&s, depth)
	case "array":
		return b.convertArray(&s, depth)
	case "string":
		return "string", nil
	case "number":
		return "number", nil
	case "integer":
		return "integer", nil
	case "boolean":
		return "boolean", nil
	case "null":
		return "null", nil
	default:
		// 无 type 或未知 type：任意值
		return "value", nil
	}
}

// convertEnum 把 enum 值转成字面量交替规则，如 ("a" | "b" | 1)。
func (b *gbnfBuilder) convertEnum(vals []json.RawMessage) (string, error) {
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		// 把每个 enum 值原样作为 JSON 字面量（compact 后转成 GBNF 字符串字面量）
		compact, err := compactJSON(v)
		if err != nil {
			return "", fmt.Errorf("gbnf: enum 值非法 JSON: %w", err)
		}
		parts = append(parts, gbnfLiteral(compact))
	}
	name := b.anon("enum")
	b.define(name, "("+strings.Join(parts, " | ")+")")
	return name, nil
}

// convertObject 生成对象规则：固定属性顺序、required 强制、可选属性用 ? 包裹。
func (b *gbnfBuilder) convertObject(s *jsonSchema, depth int) (string, error) {
	// 无 properties：退化为通用 object
	if len(s.Properties) == 0 {
		return "object", nil
	}

	// 属性名排序，保证输出稳定可测
	keys := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	reqSet := make(map[string]bool, len(s.Required))
	for _, r := range s.Required {
		reqSet[r] = true
	}

	// 为每个属性生成 "key": <valueRef> 的成员规则
	type member struct {
		body     string
		required bool
	}
	members := make([]member, 0, len(keys))
	for _, k := range keys {
		valRef, err := b.convert(s.Properties[k], depth+1)
		if err != nil {
			return "", err
		}
		body := fmt.Sprintf("%s ws %q ws \":\" ws %s", "", k, valRef)
		// 去掉前导空字符串拼接产生的多余空格
		body = strings.TrimSpace(strings.Replace(body, `"" `, "", 1))
		members = append(members, member{body: body, required: reqSet[k]})
	}

	// 组装：{ m1 ("," m2)? ... }
	// 为简化且保证可解析，按「required 在前、可选在后用逗号前缀可选」建模。
	// 形式："{" ws ( <required 用逗号连接> ( "," ws <optional> )* ) ws "}"
	var sb strings.Builder
	sb.WriteString(`"{" ws `)

	var reqMembers, optMembers []string
	for _, m := range members {
		if m.required {
			reqMembers = append(reqMembers, m.body)
		} else {
			optMembers = append(optMembers, m.body)
		}
	}

	// required 成员用逗号连接
	if len(reqMembers) > 0 {
		sb.WriteString(strings.Join(reqMembers, ` ws "," ws `))
		// 可选成员每个前面带可选逗号
		for _, om := range optMembers {
			sb.WriteString(fmt.Sprintf(` ( ws "," ws %s )?`, wrapInline(om)))
		}
	} else if len(optMembers) > 0 {
		// 无 required：第一个可选成员可有可无，后续带可选逗号
		sb.WriteString(fmt.Sprintf("( %s", wrapInline(optMembers[0])))
		for _, om := range optMembers[1:] {
			sb.WriteString(fmt.Sprintf(` ( ws "," ws %s )?`, wrapInline(om)))
		}
		sb.WriteString(" )?")
	}

	sb.WriteString(` ws "}"`)

	name := b.anon("object")
	b.define(name, sb.String())
	return name, nil
}

// convertArray 生成数组规则：根据 items 与 minItems 决定可空或至少一个元素。
func (b *gbnfBuilder) convertArray(s *jsonSchema, depth int) (string, error) {
	itemRef := "value"
	if len(s.Items) > 0 {
		ref, err := b.convert(s.Items, depth+1)
		if err != nil {
			return "", err
		}
		itemRef = ref
	}

	atLeastOne := s.MinItems != nil && *s.MinItems >= 1
	var body string
	if atLeastOne {
		// "[" ws item ( "," ws item )* ws "]"
		body = fmt.Sprintf(`"[" ws %s ( ws "," ws %s )* ws "]"`, itemRef, itemRef)
	} else {
		// "[" ws ( item ( "," ws item )* )? ws "]"
		body = fmt.Sprintf(`"[" ws ( %s ( ws "," ws %s )* )? ws "]"`, itemRef, itemRef)
	}
	name := b.anon("array")
	b.define(name, body)
	return name, nil
}

// ensurePrimitives 注册被引用到的基础规则（JSON 原语 + 空白）。
// 全部注册（render 时按使用情况输出全集，简单可靠，规则体很小）。
func (b *gbnfBuilder) ensurePrimitives() {
	// 通用 JSON value（用于降级/任意值）
	b.define("value", `object | array | string | number | boolean | null`)
	b.define("object", `"{" ws ( string ws ":" ws value ( ws "," ws string ws ":" ws value )* )? ws "}"`)
	b.define("array", `"[" ws ( value ( ws "," ws value )* )? ws "]"`)
	b.define("string", `"\"" char* "\""`)
	b.define("char", `[^"\\] | "\\" (["\\/bfnrt] | "u" hex hex hex hex)`)
	b.define("hex", `[0-9a-fA-F]`)
	b.define("number", `integer frac? exp?`)
	b.define("integer", `"-"? ( "0" | [1-9] [0-9]* )`)
	b.define("frac", `"." [0-9]+`)
	b.define("exp", `("e" | "E") ("+" | "-")? [0-9]+`)
	b.define("boolean", `"true" | "false"`)
	b.define("null", `"null"`)
	// ws：可选空白（含换行）
	b.define("ws", `[ \t\n]*`)
}

// define 登记一条命名规则（首次定义记录顺序；重复定义覆盖体但不重排）。
func (b *gbnfBuilder) define(name, body string) {
	if _, ok := b.rules[name]; !ok {
		b.order = append(b.order, name)
	}
	b.rules[name] = body
}

// anon 生成一个匿名规则名，如 "object-1"。
func (b *gbnfBuilder) anon(prefix string) string {
	b.seq++
	return fmt.Sprintf("%s-%d", prefix, b.seq)
}

// render 按定义顺序输出完整 GBNF 文本。root 永远排第一。
func (b *gbnfBuilder) render() string {
	var sb strings.Builder
	// root 优先
	if body, ok := b.rules["root"]; ok {
		sb.WriteString("root ::= ")
		sb.WriteString(body)
		sb.WriteString("\n")
	}
	for _, name := range b.order {
		if name == "root" {
			continue
		}
		sb.WriteString(name)
		sb.WriteString(" ::= ")
		sb.WriteString(b.rules[name])
		sb.WriteString("\n")
	}
	return sb.String()
}

// wrapInline 给可能含交替（|）的内联片段加括号，避免优先级歧义。
func wrapInline(s string) string {
	if strings.Contains(s, "|") && !strings.HasPrefix(strings.TrimSpace(s), "(") {
		return "( " + s + " )"
	}
	return s
}

// compactJSON 把 JSON 压成无多余空白的紧凑形式。
func compactJSON(raw json.RawMessage) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// gbnfLiteral 把一段精确文本转成 GBNF 字符串字面量（用双引号包裹并转义）。
func gbnfLiteral(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}
