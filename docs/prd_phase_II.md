**Top 5 Persian Telegram News Channels (updated 2026 data)**

These are the most popular and frequently updating Persian-language channels for Iran news (including war/conflict coverage, politics, breaking events). Rankings are based on subscriber counts, engagement, and activity from reliable directories like TGStat and Telemetr.io. They post multiple times per hour during big events (e.g., Iran war updates, attacks, international reactions). All are public and free.

1. **@akhbarefori (خبرفوری / Khabar Fori)**
   - ~5.5 million subscribers
   - Fastest breaking news (moment-to-moment updates on war, missiles, politics). Extremely active.

2. **@farsna (خبرگزاری فارس / Fars News Agency)**
   - ~1.78 million subscribers
   - Official Iranian agency – strong on military/defense news and government statements.

3. **@tasnimnews (خبرگزاری تسنیم / Tasnim News)**
   - ~2.21 million subscribers
   - High-volume war and regional news (very frequent during conflicts).

4. **@IranIntl (Iran International / ایران اینترنشنال)**
   - ~1.23 million subscribers
   - Independent/opposition-focused – excellent real-time war coverage, videos, and analysis.

5. **@VahidOnline (وحید آنلاین / Vahid Online)**
   - ~1.16 million subscribers
   - High-engagement breaking news and videos – often tops view counts during major Iran events.

**Other strong mentions** (if you want more): @BBCPersian (reputable international), @khabare_vije (خبر ویژه, ~1.1M), @iranintltv.

These update way faster than websites for live war news.

### How to Write a Golang Bot to Read Messages from These Channels

**Important limitation**: Normal Telegram **Bot API** (the one you used for Bale) **cannot** read messages from these channels. Bots can only read updates in groups/channels where they are **admins** (you can't add your bot as admin to someone else's channel).

The solution is a **Userbot** (uses your personal Telegram account via MTProto protocol). It acts like a real user, joins public channels, and listens to new posts in real-time. This is common, safe (if you use your own account), and perfect for your news crawler.

Since you want **Golang**, use the best library: **github.com/gotd/td** (modern, pure-Go, actively maintained, supports updates streaming).

#### Full Implementation Plan (Append to Your Existing Bale + Mattermost Bot)

1. **Setup (one-time)**
   - Go to https://my.telegram.org → Log in with phone → Create "App" → Get `API_ID` and `API_HASH`.
   - Add to your config (env vars or config.yaml):
     ```yaml
     TELEGRAM_API_ID: 12345678
     TELEGRAM_API_HASH: "abcdef123456..."
     TELEGRAM_PHONE: "+989123456789"   # Your number (for login)
     TELEGRAM_CHANNELS: ["akhbarefori", "farsna", "tasnimnews", "IranIntl", "VahidOnline"]
     ```

2. **Dependencies** (add to your go.mod)
   ```bash
   go get github.com/gotd/td
   go get github.com/gotd/td/telegram
   go get github.com/gotd/td/tg
   ```

3. **Core Code Structure** (add a new goroutine in your main app)

```go
package main

import (
    "context"
    "log"
    "os"
    "strings"
    "time"

    "github.com/gotd/td/telegram"
    "github.com/gotd/td/tg"
    "github.com/gotd/td/tg/tgphoto"
    // your existing Bale and Mattermost clients
)

func main() {
    // ... your existing Bale + Mattermost setup ...

    go startTelegramNewsListener() // New goroutine

    // ... keep your Bale polling running ...
}

func startTelegramNewsListener() {
    client := telegram.NewClient(os.Getenv("TELEGRAM_API_ID"), os.Getenv("TELEGRAM_API_HASH"), telegram.Options{
        // Session storage (saves login)
        SessionStorage: &telegram.FileSessionStorage{Path: "telegram.session"},
    })

    err := client.Run(context.Background(), func(ctx context.Context) error {
        // Login (first time only – it will ask for code/2FA in console or you can add flow)
        if err := client.Auth().IfNecessary(ctx); err != nil {
            log.Fatal(err)
        }

        // Join all channels (once)
        channels := strings.Split(os.Getenv("TELEGRAM_CHANNELS"), ",")
        for _, ch := range channels {
            _, err := client.API().ChannelsJoinChannel(ctx, &tg.ChannelsJoinChannelRequest{
                Channel: &tg.InputChannel{Username: strings.TrimSpace(ch)},
            })
            if err != nil {
                log.Printf("Join %s: %v (maybe already joined)", ch, err)
            }
        }

        // Listen to ALL updates
        dispatcher := tg.NewUpdateDispatcher()
        dispatcher.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, update *tg.UpdateNewChannelMessage) {
            msg := update.Message.(*tg.Message)
            if msg == nil || msg.Message == "" {
                return
            }

            // Filter only our channels (optional)
            chat, _ := e.Chat(msg.GetPeerID())
            if chat == nil {
                return
            }

            channelName := chat.GetUsername()
            if !strings.Contains(os.Getenv("TELEGRAM_CHANNELS"), channelName) {
                return
            }

            // Format nice message for Mattermost
            text := "**" + channelName + "**\n" + msg.Message + "\n\n🔗 t.me/" + channelName + "/" + fmt.Sprint(msg.ID)

            // Send to your Mattermost channel (reuse your existing poster function)
            postToMattermost(text)
        })

        // Start updates
        return client.API().UpdatesGetState(ctx) // keeps connection alive
    })

    if err != nil {
        log.Fatal(err)
    }
}
```

4. **Features You Can Add Easily**
   - Deduplication (save message ID + channel in your seen_articles.json).
   - Filter by keywords (e.g., "جنگ", "ایران", "حمله").
   - Handle photos/videos (download via `client.API().UploadGetFile`).
   - Run every 15 min fallback scraper if needed.
   - Graceful shutdown + session persistence.

5. **Advantages Over Website Crawler**
   - Real-time (seconds after post).
   - No HTML parsing headaches.
   - Includes media automatically.
   - Same app as your Bale listener.

6. **Security / Notes**
   - Use a secondary Telegram account (recommended).
   - Never share API_ID/hash.
   - First run: Bot will ask for login code in terminal (or add phone code handler).
   - Complies with Telegram ToS if not spamming.

**Ready for Dev Team Prompt?**
Just copy the above + your previous Bale/Mattermost code and say:
"Add Telegram userbot listener using gotd/td to read from these 5 channels and forward to Mattermost exactly like Bale messages."

This will give you live Iran war news in your Mattermost group with almost zero delay.

Want me to write the **full extended dev-team prompt** (including this + previous crawler) or a complete ready-to-run main.go example? Just say the word! 🚀
