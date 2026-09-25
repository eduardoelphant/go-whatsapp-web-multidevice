package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/baileysimport"
	"github.com/spf13/cobra"
)

// skipAppInitAnnotation marks commands that must not run initApp (chat
// storage, WhatsApp client and device manager bootstrap). Offline tools such
// as import-baileys only need the configuration.
const skipAppInitAnnotation = "gowa/skip-app-init"

var importBaileysOpts struct {
	input              string
	dryRun             bool
	skipSignalSessions bool
	replace            bool
}

var importBaileysCmd = &cobra.Command{
	Use:   "import-baileys",
	Short: "Import a Baileys auth state into the WhatsApp session store",
	Long: `Import a Baileys (WhiskeySockets v7) authentication state dump into the
whatsmeow store selected by --db-uri (and --db-keys-uri when set), so an
already paired companion session continues here without a new QR scan.

The dump is JSON: {"creds": <AuthenticationCreds>, "keys": {"<type>-<id>": ...}}.
Stop the Baileys client for good before importing and never run both with the
same credentials. See docs/import-baileys.md.`,
	Annotations:  map[string]string{skipAppInitAnnotation: "true"},
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runImportBaileys(cmd.Context(), cmd.OutOrStdout(), importBaileysOpts.input, importBaileysRun{
			dbURI:              config.DBURI,
			keysURI:            config.DBKeysURI,
			dryRun:             importBaileysOpts.dryRun,
			skipSignalSessions: importBaileysOpts.skipSignalSessions,
			replace:            importBaileysOpts.replace,
		})
	},
}

func init() {
	flags := importBaileysCmd.Flags()
	flags.StringVar(&importBaileysOpts.input, "input", "", `Baileys auth state JSON file --input <file> | example: --input="baileys-auth.json"`)
	flags.BoolVar(&importBaileysOpts.dryRun, "dry-run", false, "validate and convert the input, print counts only, write nothing")
	flags.BoolVar(&importBaileysOpts.skipSignalSessions, "skip-signal-sessions", false, "do not import 1:1 Signal sessions (peers re-establish them)")
	flags.BoolVar(&importBaileysOpts.replace, "replace", false, "replace an existing device with the same JID in the store")
	_ = importBaileysCmd.MarkFlagRequired("input")
	rootCmd.AddCommand(importBaileysCmd)
}

type importBaileysRun struct {
	dbURI              string
	keysURI            string
	dryRun             bool
	skipSignalSessions bool
	replace            bool
}

func runImportBaileys(ctx context.Context, out io.Writer, input string, run importBaileysRun) error {
	if ctx == nil {
		ctx = context.Background()
	}
	f, err := os.Open(input)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	state, err := baileysimport.Parse(f)
	_ = f.Close()
	if err != nil {
		return err
	}
	plan, err := baileysimport.Convert(state, baileysimport.Options{SkipSignalSessions: run.skipSignalSessions})
	if err != nil {
		return fmt.Errorf("convert: %w", err)
	}
	summary := plan.Summary()

	if run.dryRun {
		printImportSummary(out, summary)
		fmt.Fprintln(out, "dry run: nothing was written")
		return nil
	}

	target, err := openImportStore(ctx, run.dbURI)
	if err != nil {
		return fmt.Errorf("open --db-uri store: %w", err)
	}
	defer target.Close()
	var keys *baileysimport.Store
	if run.keysURI != "" && run.keysURI != run.dbURI {
		keys, err = openImportStore(ctx, run.keysURI)
		if err != nil {
			return fmt.Errorf("open --db-keys-uri store: %w", err)
		}
		defer keys.Close()
	}

	siblings, err := baileysimport.SiblingDevices(ctx, target, plan.Device.ID)
	if err != nil {
		return fmt.Errorf("list existing devices: %w", err)
	}

	err = baileysimport.Write(ctx, target, plan, baileysimport.WriteOptions{Replace: run.replace, Keys: keys})
	if errors.Is(err, baileysimport.ErrDeviceExists) {
		return fmt.Errorf("%w (use --replace to overwrite it)", err)
	} else if err != nil {
		return fmt.Errorf("import: %w", err)
	}
	printImportSummary(out, summary)
	for _, s := range siblings {
		fmt.Fprintf(out, "warning: the store also holds %s for the same number; GOWA only adopts one companion per number, remove the stale one if it is not in use\n", s)
	}
	fmt.Fprintf(out, "imported %s\n", summary.JID)
	return nil
}

func openImportStore(ctx context.Context, uri string) (*baileysimport.Store, error) {
	driver, dsn, err := whatsapp.ResolveDBDriver(uri)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	store, err := baileysimport.NewStore(ctx, db, driver, nil)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// printImportSummary prints identifiers and counts only, never key material.
func printImportSummary(out io.Writer, s baileysimport.Summary) {
	fmt.Fprintf(out, "device:                  %s\n", s.JID)
	if s.LID != "" {
		fmt.Fprintf(out, "lid:                     %s\n", s.LID)
	}
	if s.PushName != "" {
		fmt.Fprintf(out, "push name:               %s\n", s.PushName)
	}
	if s.Platform != "" {
		fmt.Fprintf(out, "platform:                %s\n", s.Platform)
	}
	fmt.Fprintf(out, "pre-keys:                %d (%d uploaded)\n", s.PreKeys, s.PreKeysUploaded)
	fmt.Fprintf(out, "signal sessions:         %d (+%d previous states)\n", s.Sessions, s.PreviousSessionStates)
	fmt.Fprintf(out, "identity keys:           %d\n", s.IdentityKeys)
	fmt.Fprintf(out, "sender keys:             %d\n", s.SenderKeys)
	fmt.Fprintf(out, "app state sync keys:     %d\n", s.AppStateSyncKeys)
	fmt.Fprintf(out, "lid mappings:            %d\n", s.LIDMappings)
	fmt.Fprintf(out, "privacy tokens:          %d\n", s.PrivacyTokens)
	if s.DroppedSessionStates+s.DroppedChains+s.DroppedMessageKeys > 0 {
		fmt.Fprintf(out, "trimmed to fit whatsmeow: %d session states, %d chains, %d message keys\n",
			s.DroppedSessionStates, s.DroppedChains, s.DroppedMessageKeys)
	}
	if line := formatCounts(s.Skipped); line != "" {
		fmt.Fprintf(out, "skipped:                 %s\n", line)
	}
	if line := formatCounts(s.Ignored); line != "" {
		fmt.Fprintf(out, "ignored:                 %s\n", line)
	}
}

func formatCounts(counts map[string]int) string {
	names := make([]string, 0, len(counts))
	for name, n := range counts {
		if n > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, counts[name]))
	}
	return strings.Join(parts, ", ")
}
