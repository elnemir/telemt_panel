package subpage

import (
	"net"
	"strconv"
	"strings"
)

const appearanceCSS = `
  .page-copy { margin: 0; max-width: 34ch; color: var(--sec); text-align: center; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 14px; }
  .account { margin: 0; color: var(--sec); font-size: 13px; overflow-wrap: anywhere; text-align: center; }
  .page-logo { display: block; width: 64px; height: 64px; object-fit: contain; }
  .support { justify-content: center; }
  .instructions .custom-copy { white-space: pre-wrap; overflow-wrap: anywhere; }
  .connection-picker {
    min-inline-size: 0; margin: 0; padding: 0; border: 0;
    display: flex; flex-wrap: wrap; gap: 8px;
  }
  .connection-picker legend { padding: 0; margin-bottom: 8px; font-size: 13px; font-weight: 600; }
  .connection-option {
    display: flex; align-items: center; max-width: 100%; min-height: 44px;
    padding: 8px 12px; border: 1px solid var(--line); border-radius: 12px;
    background: var(--sur); color: var(--txt); cursor: pointer;
    font-size: 13px; overflow-wrap: anywhere;
  }
  .connection-picker input:checked + .connection-option {
    border-color: var(--acc); color: var(--acc);
    background: color-mix(in srgb, var(--acc) 10%, var(--sur)); font-weight: 700;
  }
  .connection-picker input:focus-visible + .connection-option { outline: 2px solid var(--acc); outline-offset: 2px; }
  .connection-picker > .variant { display: none; order: 1; flex: 0 0 100%; min-width: 0; margin-top: 4px; }
  .connection-picker input:checked + .connection-option + .variant { display: flex; }
`

func hasConnectionChoice(groups []linkGroupView) bool {
	return len(groups) > 1 || (len(groups) == 1 && len(groups[0].Variants) > 1)
}

// Native radios keep selection usable in the script-free preview and by keyboard.
func renderConnectionPicker(b *pageWriter, d pageData) {
	b.writeString(`<fieldset class="connection-picker"><legend>`)
	b.writeString(pageHTMLEscaper.Replace(d.S.ConnectionChoice))
	b.writeString("</legend>")
	option := 0
	for _, group := range d.Groups {
		labels := make(map[string]int)
		for _, variant := range group.Variants {
			id := "connection-" + strconv.Itoa(option)
			label := group.Title
			if len(group.Variants) > 1 {
				address := variant.Domain
				if address == "" {
					address = variant.Server
					if variant.Port != "" {
						address = net.JoinHostPort(strings.Trim(address, "[]"), variant.Port)
					}
				}
				label += " · " + address
				labels[label]++
				if labels[label] > 1 {
					label += " (" + strconv.Itoa(labels[label]) + ")"
				}
			}
			b.writeString(`<input class="sr-only" type="radio" name="connection" id="`)
			b.writeString(id)
			b.writeString(`"`)
			if option == 0 {
				b.writeString(" checked")
			}
			b.writeString(`><label class="connection-option" for="`)
			b.writeString(id)
			b.writeString(`">`)
			b.writeString(pageHTMLEscaper.Replace(label))
			b.writeString("</label>")
			renderConnectionVariant(b, d, variant)
			option++
		}
	}
	b.writeString("</fieldset>")
}

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
