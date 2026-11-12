package subpage

const appearanceCSS = `
  .page-copy { margin: 0; max-width: 34ch; color: var(--sec); text-align: center; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 14px; }
  .account { margin: 0; color: var(--sec); font-size: 13px; overflow-wrap: anywhere; text-align: center; }
  .page-logo { display: block; width: 64px; height: 64px; object-fit: contain; }
  .support { justify-content: center; }
  .instructions .custom-copy { white-space: pre-wrap; overflow-wrap: anywhere; }
`

// Theme and accent keys are validated enums. Only constant CSS is emitted.
func renderAppearanceStyle(b *pageWriter, v *appearanceView) {
	b.writeString("<style>")
	b.writeString(appearanceCSS)
	switch v.Theme {
	case "dark":
		b.writeString(":root { color-scheme: dark; --bg:#0e1319; --sur:#171e26; --sur2:#1c242e; --line:#1f2731; --txt:#eef3f8; --sec:#8fa0b0; --mut:#7a8998; --acc:#4a9bdd; --acc-solid:#2b71ae; --ok:#5ec97e; --warn:#e0a63f; --err:#e0655a; --brand-a:#2e5c8a; --brand-b:#4a9bdd; }")
	case "light":
		b.writeString(":root { color-scheme: light; --bg:#e9edf2; --sur:#fff; --sur2:#e7ecf1; --line:#dbe2ea; --txt:#17222c; --sec:#54616f; --mut:#626d79; --acc:#1e6cb0; --acc-solid:#1e6cb0; --ok:#177a45; --warn:#8a5c0a; --err:#b53a2e; --brand-a:#244c76; --brand-b:#3880be; }")
	}
	var dark, light string
	switch v.Accent {
	case "emerald":
		dark, light = "#5ed6ad; --acc-solid:#146c55", "#17634f; --acc-solid:#17634f"
	case "violet":
		dark, light = "#c6aeff; --acc-solid:#6843a7", "#624098; --acc-solid:#624098"
	case "amber":
		dark, light = "#f0ba62; --acc-solid:#86501b", "#824810; --acc-solid:#824810"
	}
	if dark != "" {
		if v.Theme == "light" {
			b.writeString(":root{--acc:" + light + ";--brand-a:var(--acc-solid);--brand-b:var(--acc-solid)}")
		} else {
			b.writeString(":root{--acc:" + dark + ";--brand-a:var(--acc-solid);--brand-b:var(--acc-solid)}")
			if v.Theme == "auto" {
				b.writeString("@media(prefers-color-scheme:light){:root{--acc:" + light + "}}")
			}
		}
	}
	b.writeString("</style>")
}

func renderAppearanceHeader(b *pageWriter, d pageData) {
	v := d.Appearance
	if v != nil && v.LogoURI != "" {
		b.writeString(`<img class="page-logo" alt="" width="64" height="64" src="`)
		b.writeString(pageHTMLEscaper.Replace(normalizePageURL(v.LogoURI)))
		b.writeString(`">`)
	} else if v == nil || v.Logo != "hidden" {
		b.writeString(`<span class="avatar" aria-hidden="true">`)
		b.writeString(pageHTMLEscaper.Replace(d.Initial))
		b.writeString("</span>")
	}
	b.writeString("\n    <h1>")
	if v != nil && v.Copy.Title != "" {
		b.writeString(pageHTMLEscaper.Replace(v.Copy.Title))
		b.writeString("</h1>\n    <p class=\"account\">")
	}
	b.writeString(pageHTMLEscaper.Replace(d.S.AccessFor))
	b.writeString(" ")
	b.writeString(pageHTMLEscaper.Replace(d.Username))
	if v != nil && v.Copy.Title != "" {
		b.writeString("</p>")
	} else {
		b.writeString("</h1>")
	}
	if v != nil && v.Copy.Description != "" {
		b.writeString(`<p class="page-copy">`)
		b.writeString(pageHTMLEscaper.Replace(v.Copy.Description))
		b.writeString("</p>")
	}
	if d.shows("status") {
		b.writeString("\n    <span class=\"status status-")
		b.writeString(pageHTMLEscaper.Replace(d.Status.Key))
		b.writeString("\">")
		b.writeString(pageHTMLEscaper.Replace(d.Status.Label))
		b.writeString("</span>")
	}
}

func renderInstructions(b *pageWriter, instruction instructions, open bool, custom string) {
	b.writeString(`  <details class="instructions"`)
	if open {
		b.writeString(" open")
	}
	b.writeString(">\n    <summary>")
	b.writeString(pageHTMLEscaper.Replace(instruction.Heading))
	b.writeString("</summary>\n    ")
	if custom != "" {
		b.writeString(`<p class="custom-copy">`)
		b.writeString(pageHTMLEscaper.Replace(custom))
		b.writeString("</p>")
	} else {
		b.writeString("<p><strong>iOS:</strong> ")
		b.writeString(pageHTMLEscaper.Replace(instruction.IOS))
		b.writeString("</p>\n    <p><strong>Android:</strong> ")
		b.writeString(pageHTMLEscaper.Replace(instruction.Android))
		b.writeString("</p>\n    <p><strong>Desktop:</strong> ")
		b.writeString(pageHTMLEscaper.Replace(instruction.Desktop))
		b.writeString("</p>")
	}
	b.writeString("\n  </details>\n")
}
