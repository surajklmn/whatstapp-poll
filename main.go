package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/mdp/qrterminal/v3"
	"github.com/robfig/cron/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite" // Pure Go SQLite driver (no CGO compiler required)
)

// Config holds all bot settings loaded from environment or defaults
type Config struct {
	TargetGroupID   string
	CronSchedule    string
	Timezone        string
	PollQuestion    string
	PollOptions     []string
	SelectableCount int
}

func init() {
	// 1. Android / Termux SSL Certificate fallback
	if _, err := os.Stat("/data/data/com.termux/files/usr/etc/tls/cert.pem"); err == nil {
		if os.Getenv("SSL_CERT_FILE") == "" {
			_ = os.Setenv("SSL_CERT_FILE", "/data/data/com.termux/files/usr/etc/tls/cert.pem")
		}
	}

	// 2. Android / Termux DNS Resolver fallback:
	// Pure Go binaries on Android cannot find /etc/resolv.conf because Android lacks it.
	// We inject a fallback DNS resolver (1.1.1.1 and 8.8.8.8) so domain lookups work seamlessly.
	if _, err := os.Stat("/etc/resolv.conf"); os.IsNotExist(err) {
		net.DefaultResolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: 5 * time.Second}
				conn, err := d.DialContext(ctx, "udp", "1.1.1.1:53")
				if err != nil {
					return d.DialContext(ctx, "udp", "8.8.8.8:53")
				}
				return conn, nil
			},
		}
	}
}

func loadConfig() Config {
	_ = godotenv.Load()

	return Config{
		TargetGroupID:   getEnv("TARGET_GROUP_ID", ""),
		CronSchedule:    getEnv("CRON_SCHEDULE", "0 7 * * 1-5"), // 7:00 AM Mon-Fri (excluding Sat & Sun)
		Timezone:        getEnv("TIMEZONE", "Asia/Kathmandu"),
		PollQuestion:    getEnv("POLL_QUESTION", "Food today? 🍲"),
		PollOptions:     splitOptions(getEnv("POLL_OPTIONS", "Yes, No")),
		SelectableCount: getEnvInt("POLL_SELECTABLE_COUNT", 1),
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}

func splitOptions(raw string) []string {
	parts := strings.Split(raw, ",")
	var result []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	if len(result) == 0 {
		return []string{"Yes", "No"}
	}
	return result
}

// createAndSendPoll creates and sends a WhatsApp poll message to a specific chat/group
func createAndSendPoll(client *whatsmeow.Client, chat types.JID, question string, options []string, selectableCount int) error {
	pollMsg := client.BuildPollCreation(question, options, selectableCount)
	resp, err := client.SendMessage(context.Background(), chat, pollMsg)
	if err != nil {
		return err
	}
	fmt.Printf("[%s] Poll posted to %s (MsgID: %s): '%s'\n",
		time.Now().Format("15:04:05"), chat.String(), resp.ID, question)
	return nil
}

// sendTextMessage sends a simple text response
func sendTextMessage(client *whatsmeow.Client, chat types.JID, text string) {
	_, err := client.SendMessage(context.Background(), chat, &waE2E.Message{
		Conversation: proto.String(text),
	})
	if err != nil {
		fmt.Printf("Failed to send text reply: %v\n", err)
	}
}

// extractMessageText extracts plain text from incoming messages, unwrapping nested containers
func extractMessageText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if msg.DeviceSentMessage != nil && msg.DeviceSentMessage.Message != nil {
		msg = msg.DeviceSentMessage.Message
	}
	if msg.EphemeralMessage != nil && msg.EphemeralMessage.Message != nil {
		msg = msg.EphemeralMessage.Message
	}
	if msg.ViewOnceMessage != nil && msg.ViewOnceMessage.Message != nil {
		msg = msg.ViewOnceMessage.Message
	}
	if msg.ViewOnceMessageV2 != nil && msg.ViewOnceMessageV2.Message != nil {
		msg = msg.ViewOnceMessageV2.Message
	}
	if msg.Conversation != nil && *msg.Conversation != "" {
		return *msg.Conversation
	}
	if msg.ExtendedTextMessage != nil && msg.ExtendedTextMessage.Text != nil {
		return *msg.ExtendedTextMessage.Text
	}
	return ""
}

// handleIncomingMessage processes user commands in chats/groups
func handleIncomingMessage(client *whatsmeow.Client, evt *events.Message) {
	// 1. Unwrap raw message (handles Ephemeral, ViewOnce, DeviceSent)
	evt = evt.UnwrapRaw()

	// 2. Ignore messages older than 5 minutes (prevents triggering on historical backlog)
	if time.Since(evt.Info.Timestamp) > 5*time.Minute {
		return
	}

	chat := evt.Info.Chat
	// If message was typed by the user from their phone (linked device),
	// the actual destination chat JID might be in DeviceSentMessage
	if evt.RawMessage != nil && evt.RawMessage.DeviceSentMessage != nil {
		if dest := evt.RawMessage.DeviceSentMessage.GetDestinationJID(); dest != "" {
			if parsed, err := types.ParseJID(dest); err == nil && !parsed.IsEmpty() {
				chat = parsed
			}
		}
	}

	text := strings.TrimSpace(extractMessageText(evt.Message))
	if text == "" && evt.RawMessage != nil {
		text = strings.TrimSpace(extractMessageText(evt.RawMessage))
	}

	if text == "" {
		return
	}

	lowerText := strings.ToLower(text)

	// Only process bot commands starting with "/"
	if !strings.HasPrefix(lowerText, "/") {
		return
	}

	fmt.Printf("[Command Detected] Text: '%s' | Chat: %s | Sender: %s (IsFromMe: %v)\n",
		text, chat.String(), evt.Info.PushName, evt.Info.IsFromMe)

	// Command: /food
	if lowerText == "/food" || strings.HasPrefix(lowerText, "/food ") {
		fmt.Printf("[Executing /food] in %s\n", chat.String())
		err := createAndSendPoll(
			client,
			chat,
			"Food today? 🍲",
			[]string{"Yes 👍", "No ❌"},
			1,
		)
		if err != nil {
			fmt.Printf("Error creating food poll: %v\n", err)
		}
		return
	}

	// Command: /tea
	if lowerText == "/tea" || strings.HasPrefix(lowerText, "/tea ") {
		fmt.Printf("[Executing /tea] in %s\n", chat.String())
		err := createAndSendPoll(
			client,
			chat,
			"Tea time! ☕ Who is in?",
			[]string{"Yes ☕", "No ❌"},
			1,
		)
		if err != nil {
			fmt.Printf("Error creating tea poll: %v\n", err)
		}
		return
	}

	// Command: /coffee
	if lowerText == "/coffee" || strings.HasPrefix(lowerText, "/coffee ") {
		fmt.Printf("[Executing /coffee] in %s\n", chat.String())
		err := createAndSendPoll(
			client,
			chat,
			"Coffee break! ☕ Who is joining?",
			[]string{"Yes ☕", "No ❌"},
			1,
		)
		if err != nil {
			fmt.Printf("Error creating coffee poll: %v\n", err)
		}
		return
	}

	// Command: /poll <Question> | <Options...>
	if strings.HasPrefix(lowerText, "/poll") {
		fmt.Printf("[Executing /poll] in %s\n", chat.String())
		rawArgs := strings.TrimSpace(text[len("/poll"):])
		if rawArgs == "" {
			helpMsg := "📋 *Custom Poll Usage:*\n" +
				"`/poll <Question> | <Option 1>, <Option 2>, ...`\n\n" +
				"*Examples:*\n" +
				"• `/poll Lunch destination? | Pizza, Sushi, Burgers`\n" +
				"• `/poll Game night today? | Yes | No | Later`"
			sendTextMessage(client, chat, helpMsg)
			return
		}

		parts := strings.Split(rawArgs, "|")
		if len(parts) < 2 {
			sendTextMessage(client, chat, "⚠️ Please separate the question and options using a vertical bar `|`.\nExample: `/poll Lunch? | Pizza, Burger`")
			return
		}

		question := strings.TrimSpace(parts[0])
		var options []string

		// Support pipe-separated or comma-separated options
		if len(parts) == 2 && strings.Contains(parts[1], ",") {
			for _, opt := range strings.Split(parts[1], ",") {
				trimmed := strings.TrimSpace(opt)
				if trimmed != "" {
					options = append(options, trimmed)
				}
			}
		} else {
			for _, opt := range parts[1:] {
				trimmed := strings.TrimSpace(opt)
				if trimmed != "" {
					options = append(options, trimmed)
				}
			}
		}

		if len(options) < 2 {
			sendTextMessage(client, chat, "⚠️ A poll requires at least 2 options!")
			return
		}

		if len(options) > 12 {
			sendTextMessage(client, chat, "⚠️ WhatsApp allows a maximum of 12 options per poll.")
			return
		}

		err := createAndSendPoll(client, chat, question, options, 1)
		if err != nil {
			sendTextMessage(client, chat, fmt.Sprintf("❌ Failed to create poll: %v", err))
		}
		return
	}

	// Command: /help
	if lowerText == "/help" || lowerText == "/bot" {
		helpMsg := "🤖 *WhatsApp Bot Commands:*\n\n" +
			"• `/food` - Create an instant Food poll 🍲\n" +
			"• `/tea` - Create an instant Tea poll ☕\n" +
			"• `/coffee` - Create an instant Coffee poll ☕\n" +
			"• `/poll <Question> | <Opt1>, <Opt2>, ...` - Create a custom poll\n" +
			"• `/help` - Show this help message"
		sendTextMessage(client, chat, helpMsg)
		return
	}
}

func printGroups(client *whatsmeow.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	groups, err := client.GetJoinedGroups(ctx)
	if err != nil {
		fmt.Printf("Note: Could not retrieve group list: %v\n", err)
		return
	}

	fmt.Println("\n================ PARTICIPATING GROUPS ================")
	if len(groups) == 0 {
		fmt.Println("No groups found. Ensure this account has been added to the target group.")
	} else {
		for _, g := range groups {
			fmt.Printf("• %-35s -> %s\n", g.GroupName.Name, g.JID.String())
		}
	}
	fmt.Println("======================================================")
}

func main() {
	testNow := flag.Bool("test-now", false, "Send poll immediately once connected, then exit")
	flag.Parse()

	cfg := loadConfig()

	// 1. Initialize SQLite session store with WAL mode, busy timeout, and connection limit
	dbLog := waLog.Stdout("Database", "ERROR", true)
	db, err := sql.Open(
		"sqlite",
		"file:session.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)",
	)
	if err != nil {
		panic(fmt.Sprintf("Failed to open session database: %v", err))
	}
	db.SetMaxOpenConns(1) // Prevents SQLITE_BUSY / database is locked during concurrent events

	container := sqlstore.NewWithDB(db, "sqlite", dbLog)
	if err := container.Upgrade(context.Background()); err != nil {
		panic(fmt.Sprintf("Failed to upgrade session database: %v", err))
	}

	// 2. Load primary device store
	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		panic(fmt.Sprintf("Failed to get device store: %v", err))
	}

	// 3. Create whatsmeow client
	clientLog := waLog.Stdout("Client", "INFO", true)
	client := whatsmeow.NewClient(deviceStore, clientLog)

	connectedChan := make(chan struct{}, 1)

	// 4. Register event handler for incoming messages and connection lifecycle
	client.AddEventHandler(func(rawEvt interface{}) {
		switch evt := rawEvt.(type) {
		case *events.Connected:
			select {
			case connectedChan <- struct{}{}:
			default:
			}
		case *events.LoggedOut:
			fmt.Printf("\n[ALERT] Session logged out: %s\n", evt.PermanentDisconnectDescription())
			fmt.Println("Please delete session.db and restart the bot to re-authenticate.")
		case *events.Message:
			handleIncomingMessage(client, evt)
		}
	})

	// 5. Connect or initiate QR code pairing
	if client.Store.ID == nil {
		qrChan, err := client.GetQRChannel(context.Background())
		if err != nil {
			panic(fmt.Sprintf("Failed to create QR channel: %v", err))
		}

		if err = client.Connect(); err != nil {
			panic(fmt.Sprintf("Failed to connect: %v", err))
		}

		for evt := range qrChan {
			switch evt.Event {
			case "code":
				fmt.Println("\n=======================================================")
				fmt.Println("   SCAN THIS QR CODE IN WHATSAPP (Linked Devices)")
				fmt.Println("=======================================================")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
				fmt.Println("Waiting for pairing...")
			case "success":
				fmt.Println("\n[Success] Pairing complete! Saved session to session.db")
			case "timeout":
				fmt.Println("\n[Timeout] QR code expired. Retrying...")
			}
		}
	} else {
		fmt.Println("Found existing session.db. Connecting to WhatsApp...")
		if err = client.Connect(); err != nil {
			panic(fmt.Sprintf("Failed to connect: %v", err))
		}
	}

	// Wait for connection confirmation
	select {
	case <-connectedChan:
		fmt.Println("\n>>> Bot successfully connected and authenticated!")
	case <-time.After(30 * time.Second):
		if client.IsConnected() {
			fmt.Println("\n>>> Bot connected!")
		} else {
			fmt.Println("\n>>> Waiting for connection...")
		}
	}

	// Print groups
	printGroups(client)

	// Immediate test flag
	if *testNow {
		fmt.Println("\n[Mode] --test-now flag detected. Sending immediate poll test...")
		targetJID, err := types.ParseJID(cfg.TargetGroupID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Invalid TARGET_GROUP_ID: %v\n", err)
			os.Exit(1)
		}
		if err := createAndSendPoll(client, targetJID, cfg.PollQuestion, cfg.PollOptions, cfg.SelectableCount); err != nil {
			fmt.Fprintf(os.Stderr, "Error sending poll: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Test finished successfully.")
		client.Disconnect()
		return
	}

	if cfg.TargetGroupID == "" {
		fmt.Println("\n[INFO]: TARGET_GROUP_ID is empty. Daily scheduled poll is paused,")
		fmt.Println("        but chat commands like /tea, /coffee, and /poll are ACTIVE in all groups!")
	}

	// 6. Setup Cron Scheduler for Daily Poll
	var loc *time.Location
	if cfg.Timezone == "Local" || cfg.Timezone == "" {
		loc = time.Local
	} else {
		var err error
		loc, err = time.LoadLocation(cfg.Timezone)
		if err != nil {
			fmt.Printf("Warning: Invalid timezone '%s', defaulting to Local: %v\n", cfg.Timezone, err)
			loc = time.Local
		}
	}

	c := cron.New(cron.WithLocation(loc))
	if cfg.TargetGroupID != "" {
		targetJID, err := types.ParseJID(cfg.TargetGroupID)
		if err == nil {
			_, err = c.AddFunc(cfg.CronSchedule, func() {
				_ = createAndSendPoll(client, targetJID, cfg.PollQuestion, cfg.PollOptions, cfg.SelectableCount)
			})
			if err != nil {
				fmt.Printf("Failed to register cron schedule: %v\n", err)
			}
		}
	}

	c.Start()
	fmt.Printf("\n[Bot Ready & Listening for Chat Commands]\n")
	fmt.Printf("• Commands: /tea, /coffee, /poll, /help\n")
	if cfg.TargetGroupID != "" {
		fmt.Printf("• Daily Poll: '%s' (Timezone: %s) -> %s\n", cfg.CronSchedule, loc.String(), cfg.TargetGroupID)
	}
	fmt.Println("\nPress Ctrl+C to stop.")

	// Wait for OS shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nGracefully shutting down...")
	c.Stop()
	client.Disconnect()
	fmt.Println("Goodbye!")
}
