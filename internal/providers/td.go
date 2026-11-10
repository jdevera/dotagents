package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

// This is the upstream generator expression we understand, not a JavaScript
// evaluator. Any changed expression requires adapter review. Whitespace outside
// literals is significant too, favoring a visible unsupported result over drift.
const tdGenerator = "export function generateSkillFile() {\n" +
	"    const frontmatter = `---\n" +
	"name: ${SKILL_NAME}\n" +
	"description: ${JSON.stringify(SKILL_DESCRIPTION)}\n" +
	"compatibility: ${JSON.stringify(SKILL_COMPATIBILITY)}\n" +
	"license: ${packageJson.license}\n" +
	"metadata:\n" +
	"  author: Doist\n" +
	"---\n\n`;\n" +
	"    return frontmatter + SKILL_CONTENT;\n}"

func renderTD(generator, content, license string) ([]byte, error) {
	start := strings.Index(generator, "export function generateSkillFile()")
	if start < 0 || !strings.HasPrefix(generator[start:], tdGenerator+"\n") || strings.Count(generator, "export function generateSkillFile()") != 1 {
		return nil, errors.New("td generator changed; cannot safely compare generated contents with this adapter")
	}
	values := map[string]string{}
	remaining := content
	for _, name := range []string{"SKILL_NAME", "SKILL_DESCRIPTION", "SKILL_COMPATIBILITY", "SKILL_CONTENT"} {
		prefix := "export const " + name + " = "
		remaining = strings.TrimSpace(remaining)
		if !strings.HasPrefix(remaining, prefix) {
			return nil, fmt.Errorf("td constants layout changed at %s", name)
		}
		value, rest, err := jsLiteral(strings.TrimPrefix(remaining, prefix))
		if err != nil {
			return nil, fmt.Errorf("td %s: %w", name, err)
		}
		if !strings.HasPrefix(rest, ";") {
			return nil, fmt.Errorf("td %s is no longer a plain literal", name)
		}
		values[name] = value
		remaining = strings.TrimPrefix(rest, ";")
	}
	remaining = strings.TrimSpace(remaining)
	if remaining != "" && !(strings.HasPrefix(remaining, "//# sourceMappingURL=") && !strings.Contains(remaining, "\n")) {
		return nil, errors.New("td constants contain additional expressions; cannot safely compare")
	}
	if values["SKILL_NAME"] != "todoist-cli" {
		return nil, errors.New("td skill name changed; adapter review required")
	}
	result := "---\nname: " + values["SKILL_NAME"] + "\ndescription: " + jsJSON(values["SKILL_DESCRIPTION"]) +
		"\ncompatibility: " + jsJSON(values["SKILL_COMPATIBILITY"]) + "\nlicense: " + license +
		"\nmetadata:\n  author: Doist\n---\n\n" + values["SKILL_CONTENT"]
	return []byte(result), nil
}

func jsJSON(value string) string {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	result := strings.TrimSuffix(output.String(), "\n")
	// JSON.stringify emits these two valid JSON characters literally.
	result = strings.ReplaceAll(result, `\u2028`, "\u2028")
	return strings.ReplaceAll(result, `\u2029`, "\u2029")
}

// jsLiteral decodes only string literals. It never evaluates JavaScript and
// rejects template interpolation, legacy octal escapes, and unknown syntax.
func jsLiteral(input string) (string, string, error) {
	if input == "" || !strings.ContainsRune("'\"`", rune(input[0])) {
		return "", "", errors.New("expected a quoted string literal")
	}
	quote := input[0]
	var output strings.Builder
	for i := 1; i < len(input); i++ {
		c := input[i]
		if c == quote {
			return output.String(), input[i+1:], nil
		}
		if quote == '`' && c == '$' && i+1 < len(input) && input[i+1] == '{' {
			return "", "", errors.New("template interpolation is not supported")
		}
		if c != '\\' {
			if quote != '`' && (c == '\n' || c == '\r') {
				return "", "", errors.New("newline in quoted string")
			}
			output.WriteByte(c)
			continue
		}
		i++
		if i >= len(input) {
			break
		}
		switch input[i] {
		case '\\', '\'', '"', '`', '$', '/':
			output.WriteByte(input[i])
		case 'n':
			output.WriteByte('\n')
		case 'r':
			output.WriteByte('\r')
		case 't':
			output.WriteByte('\t')
		case 'b':
			output.WriteByte('\b')
		case 'f':
			output.WriteByte('\f')
		case 'v':
			output.WriteByte('\v')
		case '\n':
		case '\r':
			if i+1 < len(input) && input[i+1] == '\n' {
				i++
			}
		case 'x', 'u':
			n := 2
			if input[i] == 'u' {
				n = 4
			}
			if i+n >= len(input) {
				return "", "", errors.New("incomplete character escape")
			}
			value, err := strconv.ParseUint(input[i+1:i+1+n], 16, 32)
			if err != nil {
				return "", "", errors.New("invalid character escape")
			}
			i += n
			r := rune(value)
			if utf16.IsSurrogate(r) {
				return "", "", errors.New("surrogate escapes require adapter support")
			}
			output.WriteRune(r)
		default:
			return "", "", fmt.Errorf("unsupported escape \\%c", input[i])
		}
	}
	return "", "", errors.New("unterminated string literal")
}
