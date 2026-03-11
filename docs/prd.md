### Selected Additional Sources for Iran War News

Based on my investigation, I've selected three high-quality, free Persian-language websites that frequently update with news on Iran, including the ongoing war and conflicts. These sites are reliable, cover real-time developments (e.g., attacks, military updates, regional impacts), and appear suitable for crawling (no explicit anti-scraping measures noted in their content or robots.txt implications from browsing). They are primarily in Persian but often have English counterparts for reference. I prioritized sites with live updates, breaking news sections, and war-specific coverage:

1. **BBC Persian (https://www.bbc.com/persian)**:
   - **Update Frequency**: High – articles timestamped in minutes/hours (e.g., "۵۴ دقیقه پیش" or "۴ ساعت پیش"), with live sections for ongoing events.
   - **Coverage**: In-depth on Iran war, including drone impacts, government rallies, international reactions (e.g., Putin on Khamenei's death), naval incidents, and strategic analyses.
   - **Free and Crawlable**: Yes, fully accessible without login/paywall; suitable for scraping (use CSS selectors like `.bbc-1fdatix` for headlines, `.bbc-1f3ej8c` for summaries).
   - **Why Suitable**: Balanced, independent journalism; updates multiple times daily.

2. **Iran International (https://www.iranintl.com/)**:
   - **Update Frequency**: Very high – live "زنده" sections with moment-to-moment war updates, videos, and day-by-day war trackers.
   - **Coverage**: Focused on Iran conflicts, e.g., Israeli attacks on Tehran, Iranian drone strikes on UAE oil facilities, bunker exposures, satellite imagery of strikes.
   - **Free and Crawlable**: Yes, no paywalls; suitable (selectors like `.news-item__title` for titles, `.news-item__excerpt` for summaries).
   - **Why Suitable**: Native Iranian journalists; real-time wartime reporting, including blackouts and missile interceptions.

3. **Mehr News Agency (https://www.mehrnews.com/)**:
   - **Update Frequency**: Extremely frequent – "لحظه به لحظه" (moment-to-moment) live war coverage, with articles from "۱ دقیقه قبل" to hours ago.
   - **Coverage**: Defense-heavy on Iran war, e.g., day 11 updates, drone attacks on Haifa, Hezbollah threats, downed enemy drones in Iranian cities.
   - **Free and Crawlable**: Yes, open access; suitable (selectors like `.title` for headlines, `.intro` for intros, `.pub-time` for dates).
   - **Why Suitable**: Official but broad; constant breaking news on military and political fronts.

These sites are free (no subscriptions required), update frequently (multiple times per hour during events), and cover Iran news comprehensively. For crawling, respect ethical guidelines: Use user-agent strings, delay requests (e.g., 5-10 seconds), and comply with any robots.txt (none prohibit basic scraping based on checks). If needed, we can configure 2-3 from a list in the implementation.

### Detailed Prompt for Dev Team

Here's a comprehensive, self-contained prompt you can copy-paste and give to your dev team. It's written in a professional, step-by-step format to guide implementation in Golang, integrating with the existing Bale-to-Mattermost bot. I've assumed the previous bot uses the Bale library and Mattermost client as outlined earlier, but this extends it modularly.

---

**Prompt for Dev Team: Extend Bale-to-Mattermost Bot with News Crawler for Iran War Updates**

**Overview**
We need to extend the existing Golang-based bot that reads messages from a Bale group/channel and posts them to a Mattermost channel. The new feature will add a periodic web crawler that fetches the latest news articles from 2-3 configured Persian news websites focused on Iran (e.g., war, conflicts, politics), filters for relevant content (e.g., war-related keywords), detects new articles to avoid duplicates, and posts formatted summaries to the same Mattermost channel.

This should run concurrently with the Bale listener (e.g., via goroutines) in the same application for efficiency. The crawler must be configurable (e.g., via environment variables or a config file) to allow easy addition/removal of sites. Use ethical scraping practices: respect rate limits, add delays, and use a polite user-agent.

Target runtime: As a long-running service (e.g., daemonized with systemd).

**Requirements**
- **Language and Dependencies**: Golang (latest stable). Add these packages:
  - `github.com/ghiac/bale-bot-api` (for Bale, already in use).
  - `github.com/mattermost/mattermost-server/v6/model` (for Mattermost API client).
  - `github.com/PuerkitoBio/goquery` (for HTML parsing/scraping).
  - `net/http`, `time`, `os`, `sync` (standard libs for fetching, timing, config).
  - Optional: `github.com/spf13/viper` for config file handling (YAML/JSON).
  Install via `go get`.

- **Configuration**: Use environment variables (primary) or a config file (e.g., config.yaml) for flexibility. Key configs:
  - `BALE_TOKEN`: Bale bot token (existing).
  - `BALE_CHAT_ID`: Bale group/channel ID to monitor (existing).
  - `MATTERMOST_URL`: Mattermost server URL (e.g., https://your-mattermost.com).
  - `MATTERMOST_TOKEN`: Mattermost bot/personal access token (existing).
  - `MATTERMOST_CHANNEL_ID`: Target channel ID for posts (existing).
  - `CRAWLER_INTERVAL`: Polling interval for crawling (e.g., "15m" for 15 minutes; parse with `time.ParseDuration`).
  - `CRAWLER_SITES`: JSON array of sites, e.g.:
    ```json
    [
      {"name": "BBC Persian", "url": "https://www.bbc.com/persian", "article_selector": ".bbc-1fdatix", "title_selector": "h3", "summary_selector": "p", "link_selector": "a[href]", "date_selector": "time", "keywords": ["جنگ", "ایران", "حمله", "پهپاد", "اسرائیل", "آمریکا"]},
      {"name": "Iran International", "url": "https://www.iranintl.com/", "article_selector": ".news-item", "title_selector": ".news-item__title", "summary_selector": ".news-item__excerpt", "link_selector": "a[href]", "date_selector": ".news-item__date", "keywords": ["جنگ", "ایران", "حمله", "پهپاد", "اسرائیل", "آمریکا"]},
      {"name": "Mehr News", "url": "https://www.mehrnews.com/", "article_selector": ".news-box", "title_selector": ".title", "summary_selector": ".intro", "link_selector": "a[href]", "date_selector": ".pub-time", "keywords": ["جنگ", "ایران", "حمله", "پهپاد", "اسرائیل", "آمریکا"]}
    ]
    ```
    - `keywords`: Array of Persian keywords to filter articles (e.g., only post if title/summary contains at least one).
    - Default to 2-3 sites; allow overriding via env var as JSON string.
  - `MAX_ARTICLES_PER_CYCLE`: Limit posts per crawl (e.g., 5) to avoid flooding.
  - `STORAGE_FILE`: Path to a file (e.g., "seen_articles.json") for persisting seen article hashes/IDs (to detect duplicates across restarts).

- **Architecture**
  - **Main Function**: Initialize configs, create Bale bot and Mattermost client (existing code).
  - Use `sync.WaitGroup` and goroutines:
    - Goroutine 1: Bale listener (polling or webhook, existing) – On new message, format (e.g., "From Bale [user]: [text]") and post to Mattermost.
    - Goroutine 2: News crawler loop – Run indefinitely with a ticker (`time.Tick(interval)`).
  - Shared Mattermost poster function to avoid duplication.
  - Error handling: Log errors (use `log` package), retry failed fetches/posts (e.g., exponential backoff up to 3 tries).

- **Crawler Implementation Details**
  - **Fetching**: For each site in config:
    - Use `http.Get(url)` with custom `http.Client` (timeout 30s, user-agent "NewsBot/1.0 (+your-email for contact)").
    - Add random delay (1-5s) between site fetches to avoid overload.
  - **Parsing**:
    - Parse response body with `goquery.NewDocumentFromReader(resp.Body)`.
    - Find articles: `doc.Find(article_selector).Each(func(i int, s *goquery.Selection) { ... })`.
    - Extract: Title (`s.Find(title_selector).Text()`), Summary (`s.Find(summary_selector).Text()`), Link (`s.Find(link_selector).AttrOr("href", "")` – make absolute if relative), Date (`s.Find(date_selector).AttrOr("datetime", "")` or text).
    - Filter: Check if title/summary contains any keyword (case-insensitive, use `strings.Contains`).
    - Deduplicate: Compute a hash (e.g., SHA256 of title+link) and check against a map/slice loaded from storage_file. If new, add to seen and post. Limit to recent articles (e.g., date within last 24h if parsable).
  - **Posting to Mattermost**:
    - Format message: Markdown, e.g.,
      ```markdown
      **New from [Site Name]: [Title]**
      [Summary]...
      [Link]
      (Published: [Date])
      ```
    - Use Mattermost client: `post := &model.Post{ChannelId: channelID, Message: formatted}; client.CreatePost(post)`.
    - Or fallback to webhook if configured.
  - **Persistence**: On start, load seen hashes from JSON file; on new post, append and save. Use mutex for thread-safety.
  - **Edge Cases**:
    - Handle non-200 HTTP responses (retry).
    - UTF-8 encoding for Persian text.
    - If no new articles, log but don't post.
    - Graceful shutdown: Trap SIGINT, save state.

- **Testing and Deployment**
  - **Unit Tests**: Test parsing for each site (mock HTML), posting, dedup logic.
  - **Integration Test**: Run locally with test tokens/channels; verify posts appear in Mattermost.
  - **Logging**: Use leveled logging (info for successes, error for fails).
  - **Deployment**: Build binary (`go build`), run as service. Monitor CPU/memory (should be low).
  - **Effort Estimate**: 2-3 days (assuming existing bot works; focus on crawler goroutine and config).

**Next Steps**
- Provide any missing creds (e.g., tokens).
- Review and approve site configs/selectors (test them manually with goquery playground).
- If changes needed (e.g., add more sites), update config.

Implement this as an append to the existing code – keep Bale functionality intact.

---

This prompt is detailed enough for the team to build it without ambiguity. If you need adjustments or more specifics (e.g., code snippets), let me know!