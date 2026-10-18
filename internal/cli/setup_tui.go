package cli

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

const formWidth = 68

var (
	colorAccent = lipgloss.AdaptiveColor{Light: "#0F766E", Dark: "#2DD4BF"}
	colorWarm   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	colorText   = lipgloss.AdaptiveColor{Light: "235", Dark: "252"}
	colorMuted  = lipgloss.AdaptiveColor{Light: "243", Dark: "246"}
	colorFaint  = lipgloss.AdaptiveColor{Light: "250", Dark: "239"}
	colorOK     = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	colorError  = lipgloss.AdaptiveColor{Light: "#DC2626", Dark: "#F87171"}
	colorOnPill = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#042F2E"}

	accentStyle = lipgloss.NewStyle().Foreground(colorAccent)
	warmStyle   = lipgloss.NewStyle().Foreground(colorWarm)
	textStyle   = lipgloss.NewStyle().Foreground(colorText)
	mutedStyle  = lipgloss.NewStyle().Foreground(colorMuted)
	faintStyle  = lipgloss.NewStyle().Foreground(colorFaint)
	okStyle     = lipgloss.NewStyle().Foreground(colorOK).Bold(true)
	pillStyle   = lipgloss.NewStyle().Foreground(colorOnPill).Background(colorAccent).Bold(true).Padding(0, 1)
	cardStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorFaint).Padding(0, 1)
)

type choice struct {
	value, label, hint string
}

var aeadChoices = []choice{
	{"chacha20-poly1305", "ChaCha20-Poly1305", "recommended · fast on any CPU"},
	{"aes-128-gcm", "AES-128-GCM", "fastest on CPUs with AES-NI"},
	{"none", "None", "no record encryption · testing only"},
}

var asciiChoices = []choice{
	{"entropy", "Entropy", "high-entropy bytes both ways"},
	{"ascii", "ASCII", "printable text both ways"},
	{"up_ascii_down_entropy", "ASCII ↑  Entropy ↓", "text uplink, entropy downlink"},
	{"up_entropy_down_ascii", "Entropy ↑  ASCII ↓", "entropy uplink, text downlink"},
}

// tuiState holds the raw field values; exported fields let huh hash it for DescriptionFunc.
type tuiState struct {
	Host, Port, Name, AEAD, ASCII string
}

func runSetupWizardTUI(in *wizardInput) error {
	st := &tuiState{
		Host:  in.Host,
		Port:  strconv.Itoa(in.ServerPort),
		AEAD:  in.AEAD,
		ASCII: in.ASCIIMode,
	}
	confirmed := true

	fmt.Println()
	fmt.Println(" " + pillStyle.Render("SUDOKU") + "  " + textStyle.Bold(true).Render("Setup"))
	fmt.Println(" " + mutedStyle.Render("Writes a server and a client config, prints a short link, then starts the server."))
	fmt.Println()

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Prompt("› ").
				Title("Public address").
				Description("IP or domain that clients dial").
				Placeholder("203.0.113.10 or vpn.example.com").
				Value(&st.Host).
				Validate(validateHost),
			huh.NewInput().
				Prompt("› ").
				Title("Server port").
				Description("TCP port this server listens on").
				CharLimit(5).
				Value(&st.Port).
				Validate(func(v string) error {
					_, err := parseServerPort(v)
					return err
				}),
			huh.NewInput().
				Prompt("› ").
				Title("Node name").
				Description("Shown in clients that import the short link").
				PlaceholderFunc(func() string { return defaultNodeName(st.Host) }, &st.Host).
				CharLimit(maxNodeNameRunes).
				Value(&st.Name).
				Validate(validateNodeName),
		).Title(stepper(0)).Description(stepHint("Where clients connect and what they call this node.")),

		huh.NewGroup(
			huh.NewSelect[string]().
				Title("AEAD").
				Description("Encryption inside the Sudoku layer").
				Value(&st.AEAD).
				Options(choiceOptions(aeadChoices)...),
			huh.NewSelect[string]().
				Title("ASCII mode").
				Description("What the obfuscated bytes look like on the wire").
				Value(&st.ASCII).
				Options(choiceOptions(asciiChoices)...),
		).Title(stepper(1)).Description(stepHint("Both ends must agree; the short link carries these.")),

		huh.NewGroup(
			huh.NewSelect[bool]().
				Title("Write the configs and start the server?").
				// The static copy sizes the page up front; the summary has a fixed number of lines.
				Description(reviewSummary(st, in)).
				DescriptionFunc(func() string { return reviewSummary(st, in) }, st).
				// Value goes first: Options scrolls to whatever the accessor holds at that point.
				Value(&confirmed).
				Options(
					huh.NewOption("Start    "+mutedStyle.Render("write both configs, then launch the server"), true),
					huh.NewOption("Cancel   "+mutedStyle.Render("exit without writing anything"), false),
				),
		).Title(stepper(2)).Description(stepHint("Check everything once more; Shift+Tab goes back.")),
	).
		WithTheme(wizardTheme()).
		WithKeyMap(wizardKeyMap()).
		WithWidth(formWidth)

	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return ErrSetupCancelled
		}
		return err
	}
	if !confirmed {
		return ErrSetupCancelled
	}

	port, err := parseServerPort(st.Port)
	if err != nil {
		return fmt.Errorf("server port: %w", err)
	}
	in.Host = normalizeHost(st.Host)
	in.ServerPort = port
	in.Name = strings.TrimSpace(st.Name)
	in.AEAD = st.AEAD
	in.ASCIIMode = st.ASCII
	return nil
}

func defaultNodeName(host string) string {
	if host = normalizeHost(host); host != "" {
		return host
	}
	return "defaults to the address"
}

func choiceOptions(choices []choice) []huh.Option[string] {
	width := 0
	for _, c := range choices {
		width = max(width, lipgloss.Width(c.label))
	}
	opts := make([]huh.Option[string], 0, len(choices))
	for _, c := range choices {
		pad := strings.Repeat(" ", width-lipgloss.Width(c.label)+3)
		opts = append(opts, huh.NewOption(c.label+pad+mutedStyle.Render(c.hint), c.value))
	}
	return opts
}

func choiceLabel(choices []choice, value string) string {
	for _, c := range choices {
		if c.value == value {
			return c.label
		}
	}
	return value
}

var stepNames = []string{"Node", "Cipher", "Review"}

// stepper renders "✓ Node ── ● Cipher ── ○ Review" with the current step highlighted.
func stepper(current int) string {
	parts := make([]string, len(stepNames))
	for i, name := range stepNames {
		switch {
		case i < current:
			parts[i] = okStyle.Render("✓") + " " + mutedStyle.Render(name)
		case i == current:
			parts[i] = accentStyle.Bold(true).Render("● " + name)
		default:
			parts[i] = faintStyle.Render("○ " + name)
		}
	}
	return strings.Join(parts, faintStyle.Render("  ──  "))
}

func stepHint(text string) string {
	return mutedStyle.Render(text) + "\n"
}

// reviewSummary renders the settings card on the review page.
func reviewSummary(st *tuiState, in *wizardInput) string {
	host := normalizeHost(st.Host)
	name := cmp.Or(strings.TrimSpace(st.Name), host)
	port := strings.TrimSpace(st.Port)

	var rows []string
	row := func(k, v string) {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("%-11s", k))+v)
	}
	file := func(path string) string {
		if _, err := os.Stat(path); err == nil {
			return textStyle.Render(path) + warmStyle.Render("  overwrite")
		}
		return textStyle.Render(path)
	}

	row("Node", textStyle.Bold(true).Render(name))
	row("Address", textStyle.Render(hostPort(host, port)))
	row("Cipher", textStyle.Render(choiceLabel(aeadChoices, st.AEAD)+" · "+choiceLabel(asciiChoices, st.ASCII)))
	row("HTTP mask", textStyle.Render("auto · /"+in.PathRoot))
	row("", faintStyle.Render("server on · client config & link off"))
	row("Files", file(in.ServerPath))
	row("", file(in.ClientPath))
	row("Defaults", faintStyle.Render(fmt.Sprintf("padding %d–%d%% · pure downlink · no mux", defaultPaddingMin, defaultPaddingMax)))
	return cardStyle.Render(strings.Join(rows, "\n"))
}

func hostPort(host, port string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

func printWizardResult(w io.Writer, res *WizardResult) {
	c := res.ClientConfig
	row := func(k, v string) {
		fmt.Fprintf(w, "   %s%s\n", mutedStyle.Render(fmt.Sprintf("%-15s", k)), v)
	}

	fmt.Fprintf(w, " %s  %s %s\n", okStyle.Render("✓ Ready"), textStyle.Bold(true).Render(c.Name), mutedStyle.Render("· "+c.ServerAddress))
	fmt.Fprintln(w)
	row("Server config", textStyle.Render(res.ServerConfigPath))
	row("Client config", textStyle.Render(res.ClientConfigPath))
	row("HTTP mask", textStyle.Render("auto · /"+c.HTTPMask.PathRoot)+mutedStyle.Render("  (set disable=false in the client to use it)"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, " "+accentStyle.Bold(true).Render("Short link"))
	// No indent or frame: the link has to survive copy & paste even when the terminal wraps it.
	fmt.Fprintln(w, accentStyle.Render(res.ShortLink))
	fmt.Fprintln(w)
	fmt.Fprintf(w, " %s %s %s %s\n",
		mutedStyle.Render("Client:"), textStyle.Render(`sudoku -link "<short link>"`),
		mutedStyle.Render("or"), textStyle.Render("sudoku -c "+res.ClientConfigPath))
	fmt.Fprintln(w)
}

func wizardTheme() *huh.Theme {
	t := huh.ThemeBase()
	t.Form.Base = lipgloss.NewStyle().PaddingLeft(1)

	t.Focused.Base = t.Focused.Base.BorderForeground(colorAccent)
	t.Focused.Card = t.Focused.Base
	t.Focused.Title = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	t.Focused.NoteTitle = t.Focused.Title.MarginBottom(1)
	t.Focused.Description = lipgloss.NewStyle().Foreground(colorMuted)
	t.Focused.ErrorIndicator = lipgloss.NewStyle().Foreground(colorError).SetString(" ✗")
	t.Focused.ErrorMessage = lipgloss.NewStyle().Foreground(colorError).SetString("✗")
	t.Focused.SelectSelector = lipgloss.NewStyle().Foreground(colorWarm).SetString("› ")
	t.Focused.Option = lipgloss.NewStyle().Foreground(colorText)
	t.Focused.UnselectedOption = lipgloss.NewStyle().Foreground(colorText)
	t.Focused.SelectedOption = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(colorWarm)
	t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(colorWarm)
	t.Focused.FocusedButton = t.Focused.FocusedButton.Foreground(colorOnPill).Background(colorAccent).Bold(true)
	t.Focused.BlurredButton = t.Focused.BlurredButton.Foreground(colorMuted).Background(lipgloss.AdaptiveColor{Light: "254", Dark: "236"})
	t.Focused.Next = t.Focused.FocusedButton
	t.Focused.TextInput.Prompt = lipgloss.NewStyle().Foreground(colorWarm)
	t.Focused.TextInput.Cursor = lipgloss.NewStyle().Foreground(colorWarm)
	t.Focused.TextInput.Text = lipgloss.NewStyle().Foreground(colorText)
	t.Focused.TextInput.Placeholder = lipgloss.NewStyle().Foreground(colorFaint)

	// Blurred fields keep their layout but fade back, so the focused one stands out.
	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Blurred.Title = lipgloss.NewStyle().Foreground(colorMuted)
	t.Blurred.Description = lipgloss.NewStyle().Foreground(colorFaint)
	t.Blurred.SelectSelector = lipgloss.NewStyle().Foreground(colorMuted).SetString("› ")
	t.Blurred.SelectedOption = lipgloss.NewStyle().Foreground(colorText).Bold(true)
	t.Blurred.UnselectedOption = lipgloss.NewStyle().Foreground(colorMuted)
	t.Blurred.TextInput.Prompt = lipgloss.NewStyle().Foreground(colorFaint)
	t.Blurred.NextIndicator = lipgloss.NewStyle()
	t.Blurred.PrevIndicator = lipgloss.NewStyle()

	t.Group.Title = lipgloss.NewStyle().MarginBottom(1)
	t.Group.Description = lipgloss.NewStyle()

	t.Help.ShortKey = lipgloss.NewStyle().Foreground(colorMuted)
	t.Help.ShortDesc = lipgloss.NewStyle().Foreground(colorFaint)
	t.Help.ShortSeparator = lipgloss.NewStyle().Foreground(colorFaint)
	return t
}

func wizardKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("ctrl+c", "esc"), key.WithHelp("esc", "quit"))
	// A handful of options needs no filtering, and "/" in the help line is just noise.
	km.Select.Filter.SetEnabled(false)
	return km
}
