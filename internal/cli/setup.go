package cli

import (
	"bufio"
	"cmp"
	"crypto/rand"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/pkg/crypto"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
	"golang.org/x/term"
)

// ErrSetupCancelled is returned when the user aborts the wizard before anything is written.
var ErrSetupCancelled = errors.New("setup cancelled")

// Settings the wizard does not ask for. They match what BuildConfigFromShortLink assumes,
// so a client started from the link behaves exactly like one started from client.config.json.
const (
	defaultMixPort      = 10233
	defaultFallbackAddr = "127.0.0.1:80"
	defaultPaddingMin   = 5
	defaultPaddingMax   = 15
	defaultServerPath   = "server.config.json"
	defaultClientPath   = "client.config.json"
	maxNodeNameRunes    = 64

	// The suggested server port is a free one drawn from [minRandomPort, 65535].
	minRandomPort = 30001
	// Path roots are drawn with a random length so they do not share a fingerprint.
	minPathRootLen = 6
	maxPathRootLen = 10
)

// WizardResult aggregates outputs from the interactive setup.
type WizardResult struct {
	ServerConfig     *config.Config
	ClientConfig     *config.Config
	ServerConfigPath string
	ClientConfigPath string
	ShortLink        string
}

type wizardInput struct {
	Name       string
	Host       string
	ServerPort int
	AEAD       string
	ASCIIMode  string

	// PathRoot is generated, not asked: HTTP mask is prepared but left to the user to enable.
	PathRoot string

	ServerPath string
	ClientPath string
}

// RunSetupWizard asks for the few settings that differ between deployments, writes server and
// client configs, prints a short link and returns the server config ready to run.
func RunSetupWizard(serverPath, publicHost string) (*WizardResult, error) {
	logx.InstallStd()

	in := wizardInput{
		Host:       normalizeHost(publicHost),
		ServerPort: randomServerPort(),
		AEAD:       "chacha20-poly1305",
		ASCIIMode:  "entropy",
		PathRoot:   randomPathRoot(),
		ServerPath: cmp.Or(strings.TrimSpace(serverPath), defaultServerPath),
		ClientPath: defaultClientPath,
	}

	interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	var err error
	if interactive {
		err = runSetupWizardTUI(&in)
	} else {
		err = runSetupWizardPrompt(&in)
	}
	if err != nil {
		return nil, err
	}

	res, err := finalizeWizard(in)
	if err != nil {
		return nil, err
	}
	if interactive {
		printWizardResult(os.Stdout, res)
	} else {
		logx.Infof("Setup", "Server config saved to %s", res.ServerConfigPath)
		logx.Infof("Setup", "Client config saved to %s", res.ClientConfigPath)
		logx.Infof("Setup", "Short link: %s", res.ShortLink)
	}
	return res, nil
}

// runSetupWizardPrompt is the line-based fallback used when stdin or stdout is not a terminal.
func runSetupWizardPrompt(in *wizardInput) error {
	r := bufio.NewReader(os.Stdin)
	logx.Infof("Setup", "Sudoku setup (prompt mode)")

	in.Host = normalizeHost(promptString(r, "Public host / IP", in.Host))
	if err := validateHost(in.Host); err != nil {
		return fmt.Errorf("public host: %w", err)
	}
	port, err := parseServerPort(promptString(r, "Server port", strconv.Itoa(in.ServerPort)))
	if err != nil {
		return fmt.Errorf("server port: %w", err)
	}
	in.ServerPort = port
	in.Name = promptString(r, "Node name", in.Host)
	if err := validateNodeName(in.Name); err != nil {
		return fmt.Errorf("node name: %w", err)
	}
	in.AEAD = promptString(r, "AEAD (chacha20-poly1305 / aes-128-gcm / none)", in.AEAD)
	in.ASCIIMode = promptString(r, "ASCII mode (entropy / ascii / up_ascii_down_entropy / up_entropy_down_ascii)", in.ASCIIMode)
	return nil
}

func finalizeWizard(in wizardInput) (*WizardResult, error) {
	host := normalizeHost(in.Host)
	if err := validateHost(host); err != nil {
		return nil, fmt.Errorf("public host: %w", err)
	}
	name := cmp.Or(strings.TrimSpace(in.Name), host)

	pair, err := crypto.GenerateMasterKey()
	if err != nil {
		return nil, fmt.Errorf("generate key failed: %w", err)
	}
	key := crypto.EncodePoint(pair.Public)

	// Case, blanks and unknown enum values are normalized by Config.Finalize below.
	aead := cmp.Or(strings.TrimSpace(in.AEAD), "chacha20-poly1305")
	asciiMode := resolveASCII(in.ASCIIMode)

	// The server accepts raw Sudoku and every HTTP mask transport under the random path root.
	// Clients get the same mask settings but start disabled: flipping "disable" is all it takes
	// to move them behind a CDN later, without touching the server.
	serverMask := config.HTTPMaskConfig{Mode: "auto", PathRoot: in.PathRoot, Multiplex: "off"}
	clientMask := serverMask
	clientMask.Disable = true

	serverCfg := &config.Config{
		Name:               name,
		Mode:               "server",
		Transport:          "tcp",
		LocalPort:          in.ServerPort,
		FallbackAddr:       defaultFallbackAddr,
		Key:                key,
		AEAD:               aead,
		SuspiciousAction:   "fallback",
		PaddingMin:         defaultPaddingMin,
		PaddingMax:         defaultPaddingMax,
		ASCII:              asciiMode,
		EnablePureDownlink: true,
		HTTPMask:           serverMask,
	}
	clientCfg := &config.Config{
		Name:               name,
		Mode:               "client",
		Transport:          "tcp",
		LocalPort:          defaultMixPort,
		ServerAddress:      net.JoinHostPort(host, strconv.Itoa(in.ServerPort)),
		Key:                key,
		AEAD:               aead,
		PaddingMin:         defaultPaddingMin,
		PaddingMax:         defaultPaddingMax,
		ASCII:              asciiMode,
		ProxyMode:          "pac",
		RuleURLs:           config.DefaultPACRuleURLs(),
		EnablePureDownlink: true,
		HTTPMask:           clientMask,
	}
	if err := serverCfg.Finalize(); err != nil {
		return nil, fmt.Errorf("finalize server config: %w", err)
	}
	if err := clientCfg.Finalize(); err != nil {
		return nil, fmt.Errorf("finalize client config: %w", err)
	}

	serverPath := cmp.Or(strings.TrimSpace(in.ServerPath), defaultServerPath)
	clientPath := cmp.Or(strings.TrimSpace(in.ClientPath), defaultClientPath)
	if err := config.Save(serverPath, serverCfg); err != nil {
		return nil, fmt.Errorf("save server config: %w", err)
	}
	if err := config.Save(clientPath, clientCfg); err != nil {
		return nil, fmt.Errorf("save client config: %w", err)
	}

	shortLink, err := config.BuildShortLinkFromConfig(clientCfg, "")
	if err != nil {
		return nil, fmt.Errorf("build short link: %w", err)
	}

	return &WizardResult{
		ServerConfig:     serverCfg,
		ClientConfig:     clientCfg,
		ServerConfigPath: serverPath,
		ClientConfigPath: clientPath,
		ShortLink:        shortLink,
	}, nil
}

// normalizeHost trims blanks and IPv6 brackets so the host can be joined with a port safely.
func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return host
}

func validateHost(host string) error {
	host = normalizeHost(host)
	switch {
	case host == "":
		return errors.New("required: the IP or domain clients will dial")
	case net.ParseIP(host) != nil:
		return nil
	case strings.Contains(host, "://") || strings.Contains(host, "/"):
		return errors.New("enter a bare host, without scheme or path")
	case strings.Contains(host, ":"):
		return errors.New("enter the host without a port; the port comes next")
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("%q is not a valid IP or domain", host)
		}
		for _, c := range label {
			if !(c == '-' || c < utf8.RuneSelf && (unicode.IsLetter(c) || unicode.IsDigit(c))) {
				return fmt.Errorf("%q is not a valid IP or domain", host)
			}
		}
	}
	return nil
}

// parseServerPort also checks the port can be bound here, since the wizard starts the server next.
func parseServerPort(v string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || port <= 0 || port > 65535 {
		return 0, errors.New("must be a number from 1 to 65535")
	}
	l, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return 0, fmt.Errorf("port %d needs root here, pick one above 1023", port)
		}
		return 0, fmt.Errorf("port %d is already in use on this machine", port)
	}
	_ = l.Close()
	return port, nil
}

func validateNodeName(name string) error {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxNodeNameRunes {
		return fmt.Errorf("keep it within %d characters", maxNodeNameRunes)
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}

// randomPathRoot returns a lowercase [a-z2-7] segment of 6–10 characters, which httpmask
// accepts as a path root.
func randomPathRoot() string {
	n := minPathRootLen + mrand.IntN(maxPathRootLen-minPathRootLen+1)
	return strings.ToLower(rand.Text()[:n])
}

// randomServerPort suggests a high port that is free on this machine right now.
func randomServerPort() int {
	var port int
	for range 32 {
		port = minRandomPort + mrand.IntN(65536-minRandomPort)
		if _, err := parseServerPort(strconv.Itoa(port)); err == nil {
			break
		}
	}
	return port
}

func promptString(r *bufio.Reader, label, def string) string {
	logx.Promptf("Setup", "%s [%s]: ", label, def)
	line, _ := r.ReadString('\n')
	return cmp.Or(strings.TrimSpace(line), def)
}

func resolveASCII(val string) string {
	normalized, err := sudoku.NormalizeASCIIMode(val)
	if err == nil {
		return normalized
	}
	return "prefer_entropy"
}
