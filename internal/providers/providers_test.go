package providers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func tdFixture(t *testing.T, root, body string) {
	t.Helper()
	write(t, root, "package.json", `{"name":"@doist/todoist-cli","version":"5.12.0","license":"MIT"}`)
	installer, err := os.ReadFile("testdata/td-create-installer.js")
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "dist/lib/skills/create-installer.js", string(installer))
	write(t, root, "dist/lib/skills/content.js", "export const SKILL_NAME = 'todoist-cli';\nexport const SKILL_DESCRIPTION = 'Manage tasks';\nexport const SKILL_COMPATIBILITY = \"Requires td\";\nexport const SKILL_CONTENT = `"+body+"`;\n//# sourceMappingURL=content.js.map")
}

func TestTDProbeReconstructsGeneratedContentsAndTracksToolChanges(t *testing.T) {
	root := t.TempDir()
	tdFixture(t, root, "# Tasks\nUse \\`td\\`.\n")
	result, err := Probe(context.Background(), "td", Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	expected := "---\nname: todoist-cli\ndescription: \"Manage tasks\"\ncompatibility: \"Requires td\"\nlicense: MIT\nmetadata:\n  author: Doist\n---\n\n# Tasks\nUse `td`.\n"
	if string(result.Files["SKILL.md"].Data) != expected || result.Version != "5.12.0" {
		t.Fatalf("unexpected generation: %+v\n%s", result, result.Files["SKILL.md"].Data)
	}
	tdFixture(t, root, "# Different tool contents\n")
	changed, err := Probe(context.Background(), "td", Options{Path: root})
	if err != nil || string(changed.Files["SKILL.md"].Data) == expected {
		t.Fatalf("updated tool bundle not detected: %v", err)
	}
}

func TestTDUnknownGeneratorIsNotReportedAsClean(t *testing.T) {
	root := t.TempDir()
	tdFixture(t, root, "Hello")
	write(t, root, "dist/lib/skills/create-installer.js", strings.Replace(tdGenerator, "return frontmatter + SKILL_CONTENT", "return frontmatter + transform(SKILL_CONTENT)", 1)+"\n")
	if _, err := Probe(context.Background(), "td", Options{Path: root}); err == nil {
		t.Fatal("unsupported generator returned a plausible but incorrect skill")
	}
}

func TestTDRejectsInterpolationAndAdditionalExpressions(t *testing.T) {
	for _, body := range []string{"hello ${process.env.SECRET}", "hello`; evil(); const x = `"} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			tdFixture(t, root, body)
			if _, err := Probe(context.Background(), "td", Options{Path: root}); err == nil {
				t.Fatal("accepted executable JavaScript as plain skill text")
			}
		})
	}
}

func TestJevReadsBundleAndInstallerMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lib/python3.14/site-packages/jev_cli")
	write(t, root, "bundled_skills/jev-cli/SKILL.md", "# Jev\n")
	write(t, root, "bundled_skills/jev-cli/references/cli.md", "Reference\n")
	installer, err := os.ReadFile("testdata/jev-install-skills.py")
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "__init__.py", "INSTALL_MARKER = \".jev-cli-managed\"\nBUNDLED_SKILLS = Path(__file__).with_name(\"bundled_skills\")\n"+string(installer))
	write(t, filepath.Dir(root), "jev_cli-0.7.0.dist-info/METADATA", "Name: jev-cli\nVersion: 0.7.0\n")
	result, err := Probe(context.Background(), "jev", Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != "0.7.0" || len(result.Files) != 3 || string(result.Files[".jev-cli-managed"].Data) != "managed by jev install-skills\n" {
		t.Fatalf("bad generated tree: %+v", result)
	}
	write(t, root, "bundled_skills/jev-cli/references/cli.md", "Changed reference\n")
	changed, err := Probe(context.Background(), "jev", Options{Path: root})
	if err != nil || string(result.Files["references/cli.md"].Data) == string(changed.Files["references/cli.md"].Data) {
		t.Fatal("helper file drift not detected", err)
	}
}

func TestTDInstallerPostprocessingFailsClosed(t *testing.T) {
	for _, replacement := range []string{
		"return generateSkillFile() + 'changed after renderer';",
		"return 'a different generated skill';",
	} {
		root := t.TempDir()
		tdFixture(t, root, "Hello")
		path := filepath.Join(root, "dist/lib/skills/create-installer.js")
		original, _ := os.ReadFile(path)
		changed := strings.Replace(string(original), "return generateSkillFile();", replacement, 1)
		if changed == string(original) {
			t.Fatal("fixture did not include reviewed consumer")
		}
		write(t, root, "dist/lib/skills/create-installer.js", changed)
		if _, err := Probe(context.Background(), "td", Options{Path: root}); err == nil {
			t.Fatal("modified installer reported helper output as generated contents")
		}
	}
}

func TestJevInstallerPostprocessingFailsClosed(t *testing.T) {
	function, err := os.ReadFile("testdata/jev-install-skills.py")
	if err != nil {
		t.Fatal(err)
	}
	source := "INSTALL_MARKER = \".jev-cli-managed\"\nBUNDLED_SKILLS = Path(__file__).with_name(\"bundled_skills\")\n" + string(function)
	if err := verifyJevInstaller(source); err != nil {
		t.Fatal("reviewed fixture rejected", err)
	}
	changed := strings.Replace(source, "            staged.replace(destination)", "            (staged / 'SKILL.md').write_text('different generated contents')\n            staged.replace(destination)", 1)
	if changed == source {
		t.Fatal("fixture injection failed")
	}
	if err := verifyJevInstaller(changed); err == nil {
		t.Fatal("postprocessed bundle incorrectly reported as actual generated contents")
	}
}

func TestProbeNeverExecutesToolAndHonorsCancellation(t *testing.T) {
	home := t.TempDir()
	marker := filepath.Join(home, "executed")
	tool := write(t, home, "bin/td", "#!/bin/sh\ntouch '"+marker+"'\n")
	if err := os.Chmod(tool, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(context.Background(), "td", Options{Home: home, Path: tool}); err == nil {
		t.Fatal("missing package reported as supported")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("provider executed tool")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Probe(ctx, "td", Options{Home: home}); err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestLiteralDecoding(t *testing.T) {
	value, remaining, err := jsLiteral("`a\\`b\\n\\u00e9\\x21\\${literal}`;tail")
	if err != nil || value != "a`b\né!${literal}" || remaining != ";tail" {
		t.Fatalf("decoded %q, rest %q, err %v", value, remaining, err)
	}
	for _, input := range []string{"`unclosed", "'bad\\q'", "`${run()}`", "'\\uD800'"} {
		if _, _, err := jsLiteral(input); err == nil {
			t.Fatalf("accepted unsupported literal: %s", input)
		}
	}
}

// Opt-in integration against real installed bundles. It performs file reads
// only; CI contract tests above use isolated homes instead.
func TestInstalledToolBundles(t *testing.T) {
	home := os.Getenv("DOTAGENTS_TEST_TOOL_HOME")
	if home == "" {
		t.Skip("set DOTAGENTS_TEST_TOOL_HOME to inspect real td and jev bundles")
	}
	for _, tool := range []string{"td", "jev"} {
		t.Run(tool, func(t *testing.T) {
			result, err := Probe(context.Background(), tool, Options{Home: home})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Files["SKILL.md"].Data) == 0 || result.Version == "" {
				t.Fatalf("incomplete probe: %+v", result)
			}
			t.Logf("%s %s: %d files; %s", result.Tool, result.Version, len(result.Files), result.Method)
		})
	}
}
