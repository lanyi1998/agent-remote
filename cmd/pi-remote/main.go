package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"pi-remote/internal/buildinfo"
	"pi-remote/internal/gateway"
	"pi-remote/internal/protocol"
	"pi-remote/internal/tool"
	"pi-remote/internal/worker"
)

type toolFlags struct {
	root             string
	allowOutsideRoot bool
	runtimeDir       string
	bashPath         string
	busyBoxPath      string
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		printUsage()
		return errors.New("a subcommand is required")
	}
	switch arguments[0] {
	case "serve":
		return runServer(arguments[1:])
	case "worker":
		return runWorker(arguments[1:])
	case "version":
		fmt.Println(buildinfo.Version)
		return nil
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown subcommand %q", arguments[0])
	}
}

func runServer(arguments []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := flags.String("listen", "0.0.0.0:8787", "HTTP listen address")
	token := flags.String("token", os.Getenv("PI_REMOTE_TOKEN"), "shared encryption token (or PI_REMOTE_TOKEN)")
	toolOptions := addToolFlags(flags)
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if err := requireToken(*token); err != nil {
		return err
	}
	tools, err := createToolService(toolOptions)
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	server := gateway.New(gateway.Config{
		ListenAddress: *listen,
		Token:         *token,
		RemoteTools:   tools,
		RemoteInfo: protocol.Hello{
			ProtocolVersion: protocol.Version,
			WorkerID:        "remote",
			OS:              runtime.GOOS,
			Arch:            runtime.GOARCH,
			Hostname:        hostname,
			Root:            tools.Root(),
			Tools:           []string{"read", "bash", "edit", "write"},
			ShellProfile:    worker.ShellProfile(toolOptions.bashPath, toolOptions.busyBoxPath),
		},
	})
	ctx, stop := signalContext()
	defer stop()
	serverError := make(chan error, 1)
	log.Printf("gateway listening on %s; local workspace %s", *listen, tools.Root())
	printClientEnvironment(os.Stdout, *listen, *token)
	go func() {
		serverError <- server.ListenAndServe()
	}()
	select {
	case err := <-serverError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	}
}

func runWorker(arguments []string) error {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	serverURL := flags.String("server", "", "gateway URL, for example ws://gateway.example")
	token := flags.String("token", os.Getenv("PI_REMOTE_TOKEN"), "shared encryption token (or PI_REMOTE_TOKEN)")
	workerID := flags.String("id", "", "stable worker id (defaults to hostname)")
	toolOptions := addToolFlags(flags)
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *serverURL == "" {
		return errors.New("--server is required")
	}
	if err := requireToken(*token); err != nil {
		return err
	}
	tools, err := createToolService(toolOptions)
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	if *workerID == "" {
		*workerID = worker.DefaultWorkerID(hostname)
	}
	client := worker.New(worker.Config{
		ServerURL:    *serverURL,
		Token:        *token,
		WorkerID:     *workerID,
		Hostname:     hostname,
		ShellProfile: worker.ShellProfile(toolOptions.bashPath, toolOptions.busyBoxPath),
		Tools:        tools,
	})
	ctx, stop := signalContext()
	defer stop()
	err = client.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func addToolFlags(flags *flag.FlagSet) *toolFlags {
	options := &toolFlags{}
	flags.StringVar(&options.root, "root", ".", "workspace root")
	flags.BoolVar(&options.allowOutsideRoot, "allow-outside-root", false, "allow file tools to access paths outside the workspace")
	flags.StringVar(&options.runtimeDir, "runtime-dir", "", "base directory used to extract the embedded Windows runtime")
	flags.StringVar(&options.bashPath, "bash-path", "", "use an external bash executable instead of the embedded runtime")
	flags.StringVar(&options.busyBoxPath, "busybox-path", "", "optional external BusyBox executable")
	return options
}

func createToolService(options *toolFlags) (*tool.Service, error) {
	root, err := filepath.Abs(options.root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	return tool.NewService(tool.ServiceConfig{
		Root:             root,
		AllowOutsideRoot: options.allowOutsideRoot,
		RuntimeDir:       options.runtimeDir,
		BashPath:         options.bashPath,
		BusyBoxPath:      options.busyBoxPath,
	})
}

func requireToken(token string) error {
	if len(token) < 8 {
		return errors.New("a shared token of at least 8 characters is required")
	}
	return nil
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func printClientEnvironment(output io.Writer, listenAddress, token string) {
	clientURL, err := buildClientURL(listenAddress, preferredLocalHost)
	if err != nil {
		return
	}
	fmt.Fprintln(output, "# ________________________ Gateway ________________________")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "# Local CLI")
	fmt.Fprintf(output, "export PI_REMOTE_URL=%s\n", shellQuote(clientURL))
	fmt.Fprintf(output, "export PI_REMOTE_TOKEN=%s\n", shellQuote(token))
	fmt.Fprintln(output)
	fmt.Fprintln(output, "# Pi")
	fmt.Fprintf(output, "/remote connect %s %s\n", clientURL, token)
}

func buildClientURL(listenAddress string, wildcardHost func() string) (string, error) {
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return "", fmt.Errorf("parse listen address: %w", err)
	}
	if isWildcardHost(host) {
		host = wildcardHost()
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String(), nil
}

func preferredLocalHost() string {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	var fallback string
	for _, address := range addresses {
		ip := addressIP(address)
		if ip == nil || ip.IsLoopback() || !ip.IsGlobalUnicast() {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			if ip4.IsPrivate() {
				return ip4.String()
			}
			if fallback == "" {
				fallback = ip4.String()
			}
		}
	}
	if fallback != "" {
		return fallback
	}
	return "127.0.0.1"
}

func addressIP(address net.Addr) net.IP {
	switch value := address.(type) {
	case *net.IPNet:
		return value.IP
	case *net.IPAddr:
		return value.IP
	default:
		return nil
	}
}

func isWildcardHost(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func printUsage() {
	fmt.Println(`pi-remote - reverse-connected RPC execution for Pi agents

Usage:
  pi-remote serve [flags]
  pi-remote worker [flags]
  pi-remote version

Run "pi-remote serve -h" or "pi-remote worker -h" for flags.`)
}
