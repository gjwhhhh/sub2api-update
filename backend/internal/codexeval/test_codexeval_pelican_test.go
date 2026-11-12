// sub2api-package: backend/internal/codexeval
package codexeval

import (
	"strings"
	"testing"
)

func TestExtractHTML(t *testing.T) {
	page := "<!doctype html><svg></svg>"
	for answer, want := range map[string]string{
		"说明\n```css\nbody {}\n```\n```HTML\n" + page + "\n```": page,
		"```\n" + page + "\n```":                               page,
		"  " + page + "\n":                                     page,
		"```js\nlet svg = 1\n```\n" + page:                     "",
		"下面是页面：" + page:                                        "",
	} {
		if got := extractCode(answer, "html"); got != want {
			t.Errorf("extractHTML(%q) = %q, want %q", answer, got, want)
		}
	}
	for _, language := range []string{"html", "svg"} {
		answer := "```html\n<html>original</html>\n```\n```SVG\n<svg>composite</svg>\n```"
		want := "<html>original</html>"
		if language == "svg" {
			want = "<svg>composite</svg>"
		}
		if got := extractCode(answer, language); got != want {
			t.Fatalf("preferred %s: %q", language, got)
		}
		if got := extractCode("```"+language+"\n\n```\n```xml\n<svg/>\n```", language); got != "" {
			t.Fatalf("empty preferred block accepted: %q", got)
		}
	}
	for _, variant := range pelicanVariants {
		code := "<" + strings.Repeat("x", pelicanMaxCode-1)
		page, err := variant.page(code)
		if err != nil || !strings.HasSuffix(page, code) {
			t.Fatalf("code boundary: %s %v", variant.ID, err)
		}
		if variant.ID == "original" && page != code {
			t.Fatal("original markup changed")
		}
		if variant.ID == "all-in-one" && !strings.HasPrefix(page, "<!doctype html><style>html, body { height: 100%; margin: 0; }") {
			t.Fatal("SVG viewport wrapper missing")
		}
		if _, err := variant.page(code + "x"); err == nil {
			t.Fatal("oversized code accepted")
		}
	}
}
