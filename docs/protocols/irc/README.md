# IRC

- Status: Working
- Maintainers: @poVoq, @selfhoster1312
- Features: ???

## Configuration

> [!TIP]
> For detailed information about irc settings, see [settings.md](settings.md)

**Basic configuration example:**

```toml
[irc.myirc]
RemoteNickFormat="[{PROTOCOL}] <{NICK}> "
Server="irc.libera.chat:6667"
Nick="yourbotname"
Password="yourpassword"
# Enable SASL on modern servers like irc.libera.chat
# UseSASL=true
```

## FAQ

### Can a single bot speak as virtual users on Ergo?

Yes. `UseRelayMsg=true` uses Ergo's `RELAYMSG` to send from virtual nicknames.
Ergo enables RELAYMSG by default; matterbridge requires opting in. Under Ergo's
default configuration, give the bot channel operator status (`+o`).
See [UseRelayMsg](settings.md#userelaymsg) for setup and OneBot mentions.

### Can QQ users mention IRC users?

Set `ReverseMention=true` in the destination `[irc.myirc]` account to render
OneBot message text such as `@alice hello` as `alice hello`. Any complete IRC
nick can be used; no cross-platform identity mapping or member lookup is needed.
This setting defaults to `false`.

Optionally set `BotMentionTarget="alice"` to map a native QQ mention of the
bridge bot to a fixed IRC nick. An empty target leaves native bot mentions as
`@QQ-number` text. Other native QQ mentions stay unchanged. Settings apply to
all channels of that IRC account; other destination protocols are unaffected.

Only original message text is converted. URLs, email addresses, CQ codes,
media descriptions and quote previews stay literal. If message processing
rewrites the body before sending, conversion is skipped. Highlighting depends
on the IRC client; disabling conversion does not disable client highlights.
See [OneBot mentions](../onebot/README.md#qq--irc-的--提及) for examples.

### Can replies be preserved across bridges?

`PreserveThreading` defaults to `true` for IRC. Native replies use the IRCv3
`+draft/reply` client tag and require a server that supports `message-tags` and
assigns `msgid` values. The bridge requests `echo-message` to learn the IDs of
its own messages. Clients must support reply tags to display native references.

Replies to messages copied from another protocol resolve back to the original
message in that protocol. Mapping is scoped to each gateway, account and channel
and is held in a bounded memory cache. Replies to a split message reference its
first fragment. Missing mappings or servers without message tags use a text
quote preview. Set `PreserveThreading=false` to always use text previews.

Typing `>` or a CQ reply code remains ordinary text; use an IRCv3 client's reply
action for native references. See the [OneBot reply documentation](../onebot/README.md#引用与回复)
for QQ behavior and preview limits.

### How to connect to a password-protected channel?

```toml
[[gateway.inout]]
account="irc.myirc"
channel="#some-passworded-channel"
options = { key="password" }
```

### How to connect to OFTC-style NickServ

```toml
[irc.myirc]
Nick="yournick"
Server="irc.oftc.net:6697"
RunCommands=["PRIVMSG nickserv :IDENTIFY yourpass yournick"]
```

# FAQ

## Why can't matterbridge share files on IRC without a mediaserver?

If you see in the chat the error « Could not share file FILE (no mediaserver configured) »,
it means matterbridge tried to share a file which has no public URL when no media server
is configured. Files from networks which provide us with a public URL are unaffected
and can be shared to IRC freely.

Some files such as Matrix file attachments are shared privately and while matterbridge
can access the file content (raw bytes), there's no link to the file. In order to produce
a link that can be shared on IRC, you need to enable [a mediaserver](../../advanced/mediaserver.md),
which requires a public-facing web server.
