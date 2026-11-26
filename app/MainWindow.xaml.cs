using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text.Json.Nodes;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Shapes;
using Windows.ApplicationModel.DataTransfer;

namespace NetKey;

/// <summary>
/// The party view is a small state machine, derived every second from the service's status:
///   NoService · Idle → Waiting (join request sent) → Connecting (approved, no answer from the host yet)
///   → Connected ⇄ Reconnecting (no answer for a few seconds; amber) → Lost (no answer for 30 s; red, with the reason)
///   and back to Idle with a note when the party ends (left, kicked, closed, refused).
/// </summary>
public sealed partial class MainWindow : Window
{
    private enum View { NoService, Idle, Waiting, Connecting, Connected, Reconnecting, Lost }

    private static readonly Regex NickRe = new("^[A-Za-z0-9_-]{3,16}$");
    private const int CodeLength = 32;   // after "KEY": 8 for the party, 24 for the host key's fingerprint

    private readonly DispatcherTimer _timer = new() { Interval = TimeSpan.FromSeconds(1) };
    private readonly List<ChatItem> _chat = new();
    private string _membersKey = "";
    private bool _polling, _joining, _testing, _nameLoaded, _formatting, _wide, _chatPlaced;
    private string _status = "", _savedName = "", _secret = "", _room = "", _lastNote = "", _myNick = "";
    private View _view = View.Idle;
    private DateTime _lastOk = DateTime.MinValue, _dirLoaded = DateTime.MinValue;
    private long _chatAfter;
    private int _unread;
    private CancellationTokenSource? _chatCts, _nickCheck;
    private readonly Dictionary<string, (ulong last, DateTime at)> _activity = new();
    private readonly TrayIcon _tray;
    private readonly AppPrefs _prefs = AppPrefs.Load();
    private bool _quitting, _closeDialogOpen, _trayHinted;

    public MainWindow()
    {
        App.Log("ctor: start");
        InitializeComponent();
        App.Log("ctor: xaml loaded");
        SystemBackdrop = new MicaBackdrop();
        var ico = System.IO.Path.Combine(AppContext.BaseDirectory, "Assets", "NetKey.ico");
        TitleIcon.Source = new Microsoft.UI.Xaml.Media.Imaging.BitmapImage(new Uri(ico));
        ExtendsContentIntoTitleBar = true;
        SetTitleBar(TitleBar);
        AppWindow.SetIcon(ico);
        AppWindow.Resize(new Windows.Graphics.SizeInt32(1260, 840));
        if (AppWindow.Presenter is OverlappedPresenter p) { p.PreferredMinimumWidth = 760; p.PreferredMinimumHeight = 560; }

        // the chat card lives in one of two spots: a column on the right (wide) or under the party (narrow)
        PagesGrid.Children.Remove(ChatCard);
        TeaserIcon.Source = new Microsoft.UI.Xaml.Media.Imaging.BitmapImage(
            new Uri(System.IO.Path.Combine(AppContext.BaseDirectory, "Assets", "NetKey-256.png")));
        if (Content is FrameworkElement root)
        {
            root.ActualThemeChanged += (_, _) => SetHero();
            root.Loaded += (_, _) => SetHero();
        }
        Fit(PartyScroll, PartyColumn);
        Fit(FindPage, FindColumn);
        Fit(EmuPage, EmuColumn);
        Fit(SettingsPage, SettingsColumn);

        // closing hides the window to the notification area (or quits; asked on the first close)
        _tray = new TrayIcon(ico);
        _tray.OpenRequested += ShowWindow;
        _tray.QuitRequested += Quit;
        AppWindow.Closing += OnClosing;
        CloseChoice.SelectedIndex = _prefs.OnClose switch { "tray" => 0, "quit" => 1, _ => -1 };
        PartyPage.SizeChanged += (_, e) => Layout(e.NewSize.Width);
        Layout(1040);
        App.Log("ctor: layout done");

        // netkey.exe --page find|emulators|settings opens on that page (shortcuts, tests); select once the rail exists
        var args = Environment.GetCommandLineArgs();
        var i = Array.IndexOf(args, "--page");
        var start = i >= 0 && i + 1 < args.Length && args[i + 1] is "party" or "find" or "emulators" or "settings" ? args[i + 1] : "party";
        Nav.Loaded += (_, _) => Go(start);
        AboutText.Text = $"NetKey for Windows {typeof(App).Assembly.GetName().Version?.ToString(3)}. " +
                         "Joins LAN parties hosted on a NetKey box, for your emulators and virtual machines.";
        CheckNpcap();
        _timer.Tick += async (_, _) => await Poll();
        _timer.Start();
        _ = Poll();
        _ = LoadMe();
        App.Log("ctor: done");
    }

    // ---------------------------------------------------------------- window, tray and layout

    /// <summary>Pages are a centred column: as wide as the space allows, up to the column's MaxWidth.</summary>
    private static void Fit(ScrollViewer scroll, FrameworkElement column)
    {
        scroll.SizeChanged += (_, e) =>
        {
            var w = e.NewSize.Width - scroll.Padding.Left - scroll.Padding.Right;
            column.Width = Math.Max(0, Math.Min(column.MaxWidth, w));
        };
    }

    private void SetHero()
    {
        var dark = (Content as FrameworkElement)?.ActualTheme == ElementTheme.Dark;
        HeroImage.Source = new Microsoft.UI.Xaml.Media.Imaging.BitmapImage(
            new Uri(System.IO.Path.Combine(AppContext.BaseDirectory, "Assets", dark ? "hero-dark.png" : "hero-light.png")));
    }

    public void ShowWindow()
    {
        AppWindow.Show();
        if (AppWindow.Presenter is OverlappedPresenter op && op.State == OverlappedPresenterState.Minimized) op.Restore();
        Activate();
        _ = Poll();
    }

    private async void OnClosing(AppWindow sender, AppWindowClosingEventArgs args)
    {
        if (_quitting) return;
        args.Cancel = true;
        if (_closeDialogOpen) return;
        var choice = _prefs.OnClose;
        if (choice == "ask")
        {
            _closeDialogOpen = true;
            var remember = new CheckBox { Content = "Remember my choice (you can change it in Settings)", IsChecked = true };
            var panel = new StackPanel { Spacing = 12 };
            panel.Children.Add(new TextBlock
            {
                TextWrapping = TextWrapping.Wrap,
                Text = "Your party keeps running either way, because the NetKey service works in the background. " +
                       "In the notification area NetKey can tell you about new chat messages and bring the window back with one click.",
            });
            panel.Children.Add(remember);
            var dlg = new ContentDialog
            {
                XamlRoot = Content.XamlRoot,
                Title = "Keep NetKey in the notification area?",
                Content = panel,
                PrimaryButtonText = "Keep it there",
                SecondaryButtonText = "Quit NetKey",
                CloseButtonText = "Cancel",
                DefaultButton = ContentDialogButton.Primary,
            };
            var r = await dlg.ShowAsync();
            _closeDialogOpen = false;
            if (r == ContentDialogResult.None) return;
            choice = r == ContentDialogResult.Primary ? "tray" : "quit";
            if (remember.IsChecked == true)
            {
                _prefs.OnClose = choice;
                _prefs.Save();
                CloseChoice.SelectedIndex = choice == "tray" ? 0 : 1;
            }
        }
        if (choice == "quit") { Quit(); return; }
        AppWindow.Hide();
        if (!_trayHinted)
        {
            _trayHinted = true;
            _tray.Notify("NetKey is still here", "Click the NetKey icon in the notification area to open it again.");
        }
    }

    private void Quit()
    {
        _quitting = true;
        _timer.Stop();
        StopChat();
        _tray.Dispose();
        Application.Current.Exit();
    }

    private void CloseChoice_SelectionChanged(object sender, SelectionChangedEventArgs e)
    {
        var v = CloseChoice.SelectedIndex switch { 0 => "tray", 1 => "quit", _ => _prefs.OnClose };
        if (v == _prefs.OnClose) return;
        _prefs.OnClose = v;
        _prefs.Save();
    }

    private bool WindowHidden => !AppWindow.IsVisible;

    // ---------------------------------------------------------------- layout and navigation

    private void Layout(double width)
    {
        var wide = width >= 900;
        if (_chatPlaced && wide == _wide) return;
        _chatPlaced = true;
        _wide = wide;
        NarrowChatHost.Child = null;
        WideChatHost.Child = null;
        if (wide)
        {
            ChatCard.Height = double.NaN;
            WideChatHost.Child = ChatCard;
        }
        else
        {
            ChatCard.Height = 460;
            NarrowChatHost.Child = ChatCard;
        }
        ChatColumn.Width = new GridLength(wide && ChatCard.Visibility == Visibility.Visible ? 360 : 0);
    }

    private void Nav_SelectionChanged(NavigationView sender, NavigationViewSelectionChangedEventArgs args)
    {
        var tag = args.IsSettingsSelected ? "settings" : (args.SelectedItem as NavigationViewItem)?.Tag as string ?? "party";
        PartyPage.Visibility = tag == "party" ? Visibility.Visible : Visibility.Collapsed;
        FindPage.Visibility = tag == "find" ? Visibility.Visible : Visibility.Collapsed;
        EmuPage.Visibility = tag == "emulators" ? Visibility.Visible : Visibility.Collapsed;
        SettingsPage.Visibility = tag == "settings" ? Visibility.Visible : Visibility.Collapsed;
        if (tag == "party") { _unread = 0; NavBadge.Visibility = Visibility.Collapsed; }
        if (tag == "find") _ = LoadDirectory();
        if (tag == "settings") _ = LoadMe();
        if (tag == "emulators") CheckNpcap();
    }

    private void Go(string tag) =>
        Nav.SelectedItem = tag == "settings" ? Nav.SettingsItem
            : Nav.MenuItems.OfType<NavigationViewItem>().First(i => (string)i.Tag == tag);

    private void GoFind_Click(object sender, RoutedEventArgs e) => Go("find");
    private void GoEmulators_Click(object sender, RoutedEventArgs e) => Go("emulators");
    private void GoSettings_Click(object sender, RoutedEventArgs e) => Go("settings");

    private static void Bar(InfoBar bar, bool open)
    {
        bar.IsOpen = open;
        bar.Visibility = open ? Visibility.Visible : Visibility.Collapsed;
    }

    private void Bar_Closed(InfoBar sender, InfoBarClosedEventArgs args) => sender.Visibility = Visibility.Collapsed;

    private static void ShowError(InfoBar bar, string message)
    {
        bar.Message = message;
        Bar(bar, true);
    }

    private static Brush Res(string key) => (Brush)Application.Current.Resources[key];

    // ---------------------------------------------------------------- status

    private async Task Poll()
    {
        if (_polling) return;
        _polling = true;
        try { Show(await ServiceClient.StatusAsync()); }
        finally { _polling = false; }
    }

    private static DateTime ParseTime(string s) =>
        DateTime.TryParse(s, null, System.Globalization.DateTimeStyles.AdjustToUniversal, out var t) ? t : DateTime.MinValue;

    private void Show(JsonObject s)
    {
        if (!s.Bool("ok"))
        {
            _view = View.NoService;
            Bar(ServiceBar, true);
            JoinButton.IsEnabled = false;
            ServiceText.Text = "Not running. Start “NetKey” in Windows Services, or reinstall NetKey.";
            return;
        }
        Bar(ServiceBar, false);
        ServiceText.Text = $"Running (version {s.Str("version")}). It keeps your party going while this window is closed.";

        _savedName = s.Str("name");
        if (!_nameLoaded) { NameBox.Text = _savedName; _nameLoaded = true; }
        JoinAsName.Text = _savedName;
        _secret = s.Str("secret");
        SecretText.Text = _secret;
        NetCfg.Text = "Link Driver NE2000\n    PORT 300\n    IRQ 10\n    FRAME Ethernet_802.2";
        DosboxConf.Text = "[ne2000]\nne2000=true\nnicbase=300\nnicirq=10\nmacaddr=" + s.Str("dosbox_mac") +
                          "\nbackend=pcap\n\n[ethernet, pcap]\nrealnic=NetKey";

        var party = s["party"];
        var prev = _status;
        _status = party.Str("status");
        _lastOk = ParseTime(party.Str("last_ok"));

        // notes from the service say why a party ended (left, kicked, closed, refused): show the newest once
        if (s["notes"] is JsonArray notes && notes.Count > 0)
        {
            var note = notes[^1]?.GetValue<string>() ?? "";
            if (_status == "" && prev != "" && note != _lastNote)
            {
                var left = note.StartsWith("Left");
                NoteBar.Title = left ? "You left the party" : "The party ended";
                NoteBar.Message = note;
                NoteBar.Severity = left ? InfoBarSeverity.Informational : InfoBarSeverity.Warning;
                Bar(NoteBar, true);
                if (WindowHidden) _tray.Notify(NoteBar.Title, note);
            }
            _lastNote = note;
        }

        _view = _status switch
        {
            "" => View.Idle,
            "joining" => View.Waiting,
            _ when party.Bool("online") => View.Connected,
            _ when _lastOk == DateTime.MinValue => View.Connecting,
            _ when DateTime.UtcNow - _lastOk < TimeSpan.FromSeconds(30) => View.Reconnecting,
            _ => View.Lost,
        };
        IdleView.Visibility = _view == View.Idle ? Visibility.Visible : Visibility.Collapsed;
        WaitView.Visibility = _view == View.Waiting ? Visibility.Visible : Visibility.Collapsed;
        var inParty = _view is View.Connecting or View.Connected or View.Reconnecting or View.Lost;
        PartyView.Visibility = inParty ? Visibility.Visible : Visibility.Collapsed;
        ChatCard.Visibility = inParty ? Visibility.Visible : Visibility.Collapsed;
        var col = inParty && _wide ? 360 : 0;
        if (ChatColumn.Width.Value != col) ChatColumn.Width = new GridLength(col);
        if (_view == View.Idle) Bar(WarnBar, false);
        JoinButton.IsEnabled = !_joining && CodeComplete(CodeBox.Text);

        var partyName = party.Str("party_name");
        _tray.Tooltip = _view switch
        {
            View.Connected => $"NetKey · in {partyName}'s party",
            View.Waiting => $"NetKey · waiting for {partyName}",
            View.Connecting or View.Reconnecting => $"NetKey · connecting to {partyName}",
            View.Lost => "NetKey · connection lost",
            _ => "NetKey · not in a party",
        };
        WaitTitle.Text = $"Waiting for {partyName} to let you in";
        KeyCheck.Text = party.Str("key_check");
        PartyTitle.Text = partyName;
        ShowChip(party);

        var rtt = party.Num("rtt_ms");
        StatPing.Text = _view == View.Connected && rtt > 0 ? $"{rtt:0} ms" : "–";
        var relay = party.Str("relay");
        StatRelay.Text = relay != "" ? relay : "hub";

        var members = new List<MemberItem>();
        if (partyName != "") members.Add(new MemberItem { Name = partyName, Tag = "Host" });
        var myId = (int)party.Num("member_id");
        if (party?["members"] is JsonArray arr)
            foreach (var m in arr)
                members.Add(new MemberItem { Name = m.Str("name"), Tag = (int)m.Num("id") == myId ? "You" : "" });
        var key = string.Join("\n", members.Select(m => m.Name + "|" + m.Tag));
        if (key != _membersKey)
        {
            _membersKey = key;
            Members.Children.Clear();
            foreach (var m in members) Members.Children.Add(MemberRow(m));
        }
        StatPlayers.Text = members.Count.ToString();

        var files = party?["files"];
        FilesText.Visibility = files.Str("share") != "" ? Visibility.Visible : Visibility.Collapsed;
        FilesText.Text = $"The host shares files: \\\\{files.Str("name")}\\{files.Str("share")}  (or {files.Str("address")})";

        ShowActivity(s);
        ShowTest(s);

        if (s["warnings"] is JsonArray warnings && warnings.Count > 0)
        {
            WarnBar.Message = warnings[^1]?.GetValue<string>() ?? "";
            Bar(WarnBar, true);
        }

        // the chat follows the party: (re)start when the room changes, stop outside a party
        var room = inParty ? (party.Bool("public") ? "l:" + party.Str("listing") : "p:" + partyName) : "";
        if (room != _room)
        {
            _room = room;
            StopChat();
            if (room != "") StartChat();
        }
    }

    private void ShowChip(JsonNode? party)
    {
        (string text, string bg, string dot, string line) = _view switch
        {
            View.Connected => ("Connected", "SystemFillColorSuccessBackgroundBrush", "SystemFillColorSuccessBrush",
                "You're on the party's network. Start a game and choose network play."),
            View.Connecting => ("Connecting", "SubtleFillColorSecondaryBrush", "AccentFillColorDefaultBrush",
                "You're in! Setting up the encrypted link to the host…"),
            View.Reconnecting => ("Reconnecting", "SystemFillColorCautionBackgroundBrush", "SystemFillColorCautionBrush",
                "The host hasn't answered for a few seconds. Trying again…"),
            View.Lost => ("Connection lost", "SystemFillColorCriticalBackgroundBrush", "SystemFillColorCriticalBrush",
                (party.Str("error") is { Length: > 0 } e ? e + ". " : "") +
                "Retrying every few seconds. Check your internet connection, or ask the host whether their box is online."),
            _ => ("", "SubtleFillColorSecondaryBrush", "AccentFillColorDefaultBrush", ""),
        };
        ChipText.Text = text;
        Chip.Background = Res(bg);
        ChipDot.Fill = Res(dot);
        PartyLine.Text = line;
    }

    private void ShowActivity(JsonObject s)
    {
        var frames = s["frames"];
        void One(string port, bool open, string error, Ellipse dot, TextBlock label)
        {
            var count = frames?[port]?["In"] is JsonValue v && v.TryGetValue<double>(out var c) ? (ulong)c : 0;
            if (!_activity.TryGetValue(port, out var a) || count < a.last) a = (count, DateTime.MinValue);
            if (count > a.last) a = (count, DateTime.UtcNow);
            _activity[port] = a;
            if (!open && error != "")
            {
                dot.Fill = Res("SystemFillColorCriticalBrush");
                label.Text = "Not available";
                ToolTipService.SetToolTip(label, error);
            }
            else if (open && DateTime.UtcNow - a.at < TimeSpan.FromSeconds(15))
            {
                dot.Fill = Res("SystemFillColorSuccessBrush");
                label.Text = "Traffic flowing";
            }
            else
            {
                dot.Fill = Res("ControlStrongFillColorDisabledBrush");
                label.Text = open ? "Ready, no traffic yet" : "Off";
            }
        }
        One("tap", s["adapter"].Bool("connected"), s["adapter"].Str("error"), TapDot, TapState);
        One("86box", s["lswitch"].Bool("open"), s["lswitch"].Str("error"), LsDot, LsState);

        // DOSBox IPX: how many DOSBox are connected, and whether they're sending
        var ipx = s["ipx"];
        var clients = (ipx?["clients"] as JsonArray)?.Count ?? 0;
        One("ipx", ipx.Bool("listening"), ipx.Str("error"), IpxDot, IpxState);
        if (ipx.Bool("listening") && IpxState.Text != "Not available")
        {
            var n = clients == 1 ? "1 DOSBox" : $"{clients} DOSBox";
            IpxState.Text = clients == 0 ? "Waiting for DOSBox"
                : IpxState.Text == "Traffic flowing" ? $"{n} · traffic flowing" : $"{n} connected";
            if (clients > 0 && IpxState.Text.EndsWith("connected")) IpxDot.Fill = Res("AccentFillColorDefaultBrush");
        }
    }

    private void ShowTest(JsonObject s)
    {
        TestButton.IsEnabled = _view == View.Connected && !_testing && !s.Bool("testing");
        if (_testing) return;
        if (s["test"] is JsonObject t)
            TestText.Text = t.Bool("ok")
                ? $"Passed. A test PC got {t.Str("address")} and the host answered in {t.Num("ping_ms"):0} ms, full-size packets too."
                : "Failed: " + t.Str("error");
    }

    // ---------------------------------------------------------------- joining with a code

    private static string CodeBody(string text)
    {
        var raw = new string(text.ToUpperInvariant().Where(char.IsLetterOrDigit).ToArray());
        if (raw.StartsWith("KEY")) raw = raw[3..];
        return raw.Length > CodeLength ? raw[..CodeLength] : raw;
    }

    private static bool CodeComplete(string text) => CodeBody(text).Length == CodeLength;

    private void CodeBox_TextChanged(object sender, TextChangedEventArgs e)
    {
        if (_formatting) return;
        var body = CodeBody(CodeBox.Text);
        var formatted = body.Length == 0 ? "" : "KEY-" + string.Join("-", Enumerable.Range(0, (body.Length + 3) / 4)
            .Select(i => body.Substring(i * 4, Math.Min(4, body.Length - i * 4))));
        if (formatted != CodeBox.Text && CodeBox.SelectionStart >= CodeBox.Text.Length)
        {
            _formatting = true;
            CodeBox.Text = formatted;
            CodeBox.SelectionStart = formatted.Length;
            _formatting = false;
        }
        var n = body.Length;
        CodeHint.Text = n == 0 ? "The host finds it on their Parties page. It starts with KEY-."
            : n < CodeLength ? $"{CodeLength - n} more characters"
            : "Looks complete.";
        CodeHint.Foreground = Res(n == CodeLength ? "SystemFillColorSuccessBrush" : "TextFillColorSecondaryBrush");
        JoinButton.IsEnabled = !_joining && n == CodeLength && _view != View.NoService;
    }

    private async void Paste_Click(object sender, RoutedEventArgs e)
    {
        var content = Clipboard.GetContent();
        if (content.Contains(StandardDataFormats.Text))
        {
            CodeBox.Text = await content.GetTextAsync();
            CodeBox.Focus(FocusState.Programmatic);
        }
    }

    private void CodeBox_KeyDown(object sender, KeyRoutedEventArgs e)
    {
        if (e.Key == Windows.System.VirtualKey.Enter && JoinButton.IsEnabled) Join_Click(sender, e);
    }

    private void SetJoining(bool on)
    {
        _joining = on;
        JoinRing.IsActive = on;
        JoinRing.Visibility = on ? Visibility.Visible : Visibility.Collapsed;
        JoinLabel.Text = on ? "Asking the host…" : "Join party";
        JoinButton.IsEnabled = !on && CodeComplete(CodeBox.Text);
    }

    private async void Join_Click(object sender, RoutedEventArgs e)
    {
        Bar(ErrorBar, false);
        Bar(NoteBar, false);
        SetJoining(true);
        var r = await ServiceClient.JoinAsync(CodeBox.Text, _savedName);
        SetJoining(false);
        if (!r.Bool("ok")) ShowError(ErrorBar, r.Str("error"));
        else CodeBox.Text = "";
        await Poll();
    }

    // ---------------------------------------------------------------- leaving and testing

    private async void Leave_Click(object sender, RoutedEventArgs e)
    {
        Bar(ErrorBar, false);
        if (_status == "approved")
        {
            var dlg = new ContentDialog
            {
                XamlRoot = Content.XamlRoot,
                Title = $"Leave {PartyTitle.Text}?",
                Content = "Your emulators lose the connection to everyone in the party. To come back, the host has to let you in again.",
                PrimaryButtonText = "Leave",
                CloseButtonText = "Stay",
                DefaultButton = ContentDialogButton.Close,
            };
            if (await dlg.ShowAsync() != ContentDialogResult.Primary) return;
        }
        var r = await ServiceClient.LeaveAsync();
        if (!r.Bool("ok")) ShowError(ErrorBar, r.Str("error"));
        await Poll();
    }

    private async void Test_Click(object sender, RoutedEventArgs e)
    {
        _testing = true;
        TestButton.IsEnabled = false;
        TestRing.IsActive = true;
        TestRing.Visibility = Visibility.Visible;
        TestLabel.Text = "Testing…";
        TestText.Text = "A test PC is asking the party for an address and pinging the host…";
        await ServiceClient.TestAsync();
        _testing = false;
        TestRing.IsActive = false;
        TestRing.Visibility = Visibility.Collapsed;
        TestLabel.Text = "Run again";
        await Poll();
    }

    // ---------------------------------------------------------------- chat

    private void StartChat()
    {
        _chat.Clear();
        ChatPanel.Children.Clear();
        _chatAfter = 0;
        _unread = 0;
        ChatEmpty.Visibility = Visibility.Visible;
        SetChatPermission(true, "");
        var cts = _chatCts = new CancellationTokenSource();
        _ = ChatLoop(cts.Token);
    }

    private void StopChat()
    {
        _chatCts?.Cancel();
        _chatCts = null;
        _chat.Clear();
        ChatPanel.Children.Clear();
        NavBadge.Visibility = Visibility.Collapsed;
    }

    private async Task ChatLoop(CancellationToken ct)
    {
        var backoff = 2;
        var first = true;
        while (!ct.IsCancellationRequested)
        {
            var r = await ServiceClient.ChatAsync(_chatAfter, first ? 0 : 15);
            if (ct.IsCancellationRequested) return;
            if (!r.Bool("ok"))
            {
                var err = r.Str("error");
                SetChatPermission(false, err.Contains("turned the chat off") ? "The host turned the chat off for this party." : err);
                await Task.Delay(TimeSpan.FromSeconds(backoff));
                backoff = Math.Min(backoff * 2, 30);
                continue;
            }
            backoff = 2;
            SetChatPermission(r.Bool("can_post"), r.Str("why"));
            if (r["deleted"] is JsonArray del)
                foreach (var id in del.Select(d => d is JsonValue v && v.TryGetValue<double>(out var x) ? (long)x : 0))
                    if (_chat.FindIndex(c => c.Id == id) is var i and >= 0)
                    {
                        _chat.RemoveAt(i);
                        ChatPanel.Children.RemoveAt(i);
                    }
            var added = 0;
            if (r["messages"] is JsonArray msgs)
                foreach (var m in msgs)
                {
                    var item = ChatItem.From(m!);
                    if (_chat.Any(c => c.Id == item.Id)) continue;
                    _chat.Add(item);
                    ChatPanel.Children.Add(ChatRow(item));
                    if (!item.IsMe && !first)
                    {
                        added++;
                        if (WindowHidden) _tray.Notify(item.Nick, item.Text);
                    }
                }
            _chatAfter = Math.Max(_chatAfter, (long)r.Num("last"));
            first = false;
            ChatEmpty.Visibility = _chat.Count == 0 ? Visibility.Visible : Visibility.Collapsed;
            if (added > 0 || first || _chat.Count > 0)
                DispatcherQueue.TryEnqueue(Microsoft.UI.Dispatching.DispatcherQueuePriority.Low,
                    () => ChatScroll.ChangeView(null, ChatScroll.ScrollableHeight, null, true));
            if (added > 0 && PartyPage.Visibility != Visibility.Visible)
            {
                _unread += added;
                NavBadge.Value = _unread;
                NavBadge.Visibility = Visibility.Visible;
            }
        }
    }

    private void SetChatPermission(bool canPost, string why)
    {
        ChatBox.IsEnabled = canPost;
        ChatSend.IsEnabled = canPost;
        ChatWhy.Text = why;
        ChatWhy.Visibility = canPost || why == "" ? Visibility.Collapsed : Visibility.Visible;
        ChatBox.PlaceholderText = canPost ? "Message the party" : "You can't post in this chat";
    }

    private async void ChatSend_Click(object sender, RoutedEventArgs e)
    {
        var text = ChatBox.Text.Trim();
        if (text == "" || !ChatBox.IsEnabled) return;
        ChatSend.IsEnabled = false;
        var r = await ServiceClient.ChatSendAsync(text);
        ChatSend.IsEnabled = ChatBox.IsEnabled;
        if (r.Bool("ok"))
        {
            ChatBox.Text = "";
            ChatWhy.Visibility = Visibility.Collapsed;
        }
        else
        {
            ChatWhy.Text = r.Str("error") == "slow down a little" ? "Slow down a little: one message every 2 seconds." : r.Str("error");
            ChatWhy.Visibility = Visibility.Visible;
        }
    }

    private void ChatBox_KeyDown(object sender, KeyRoutedEventArgs e)
    {
        if (e.Key == Windows.System.VirtualKey.Enter) { e.Handled = true; ChatSend_Click(sender, e); }
    }

    private async void ReportMessage(ChatItem item)
    {
        var id = item.Id;
        var reason = new TextBox { PlaceholderText = "What's wrong with it? (optional)", MaxLength = 300, AcceptsReturn = true, Height = 90, TextWrapping = TextWrapping.Wrap };
        var panel = new StackPanel { Spacing = 10 };
        panel.Children.Add(new TextBlock { Text = "The NetKey team reads reports and can ban people from public parties. The sender isn't told who reported them.", TextWrapping = TextWrapping.Wrap });
        panel.Children.Add(reason);
        var dlg = new ContentDialog
        {
            XamlRoot = Content.XamlRoot,
            Title = "Report this message?",
            Content = panel,
            PrimaryButtonText = "Report",
            CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Close,
        };
        if (await dlg.ShowAsync() != ContentDialogResult.Primary) return;
        var r = await ServiceClient.ReportAsync(id, reason.Text.Trim());
        ChatWhy.Text = r.Bool("ok") ? "Thanks, the report was sent." : "Couldn't send the report: " + r.Str("error");
        ChatWhy.Visibility = Visibility.Visible;
    }

    // ---------------------------------------------------------------- public parties and nickname

    private async Task LoadMe()
    {
        var me = await ServiceClient.MeAsync();
        if (!me.Bool("ok")) return;
        _myNick = me.Str("nick");
        var ban = me["ban"] as JsonObject;
        if (ban is not null)
        {
            BanBar.Message = (ban.Str("reason") is { Length: > 0 } why ? why + ". " : "") +
                             (ban["until"] is JsonValue u && u.TryGetValue<double>(out var until)
                                 ? "Until " + DateTimeOffset.FromUnixTimeSeconds((long)until).ToLocalTime().ToString("d MMM yyyy") + "."
                                 : "");
            Bar(BanBar, true);
        }
        else Bar(BanBar, false);
        NickGate.Visibility = _myNick == "" && ban is null ? Visibility.Visible : Visibility.Collapsed;

        double? canAt = me["can_change_at"] is JsonValue cv && cv.TryGetValue<double>(out var ts) ? ts : null;
        if (_myNick == "")
        {
            NickInfo.Text = "You don't have one yet. You need it to join public parties and to chat in them. It can be changed once every 30 days.";
            NickChange.Content = "Choose";
        }
        else
        {
            NickInfo.Text = $"You're “{_myNick}” in public parties and their chats. " + (canAt is { } c
                ? $"You can change it again on {DateTimeOffset.FromUnixTimeSeconds((long)c).ToLocalTime():d MMM yyyy}."
                : "You can change it now; then not again for 30 days, and the old one stays reserved for you for 30 days.");
            NickChange.Content = "Change";
        }
        NickEdit.IsEnabled = canAt is null;
        if (NickEdit.Text == "") NickEdit.Text = _myNick;
    }

    private async Task LoadDirectory()
    {
        if (DateTime.UtcNow - _dirLoaded < TimeSpan.FromSeconds(3)) return;
        _dirLoaded = DateTime.UtcNow;
        Bar(FindError, false);
        FindRing.IsActive = true;
        FindRing.Visibility = Visibility.Visible;
        await LoadMe();
        var d = await ServiceClient.DirectoryAsync();
        FindRing.IsActive = false;
        FindRing.Visibility = Visibility.Collapsed;
        if (!d.Bool("ok"))
        {
            ShowError(FindError, d.Str("error"));
            return;
        }
        var inParty = _status != "";
        var canJoin = !inParty && _myNick != "" && BanBar.Visibility != Visibility.Visible;
        var live = (d["live"] as JsonArray)?.Select(n => ListingItem.From(n!)).ToList() ?? new();
        LiveList.Children.Clear();
        foreach (var l in live)
        {
            l.CanJoin = canJoin && !l.Full && !l.Joined && !l.Mine;
            LiveList.Children.Add(ListingCard(l));
        }
        LiveEmpty.Visibility = live.Count == 0 ? Visibility.Visible : Visibility.Collapsed;
        var upcoming = (d["upcoming"] as JsonArray)?.Select(n => ListingItem.From(n!)).ToList() ?? new();
        UpcomingList.Children.Clear();
        foreach (var u in upcoming) UpcomingList.Children.Add(UpcomingCard(u));
        UpcomingHeader.Visibility = UpcomingNote.Visibility = upcoming.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
        LiveHeader.Text = inParty ? "Live now · leave your current party to join another" : "Live now";
    }

    private void Refresh_Click(object sender, RoutedEventArgs e)
    {
        _dirLoaded = DateTime.MinValue;
        _ = LoadDirectory();
    }

    private async Task JoinListing(Button b, string id)
    {
        b.IsEnabled = false;
        b.Content = "Asking…";
        var r = await ServiceClient.JoinListingAsync(id);
        if (!r.Bool("ok"))
        {
            ShowError(FindError, r.Str("error"));
            b.Content = "Join";
            b.IsEnabled = true;
            return;
        }
        Go("party");
        await Poll();
    }

    private async Task<string> NickProblem(string nick)
    {
        if (!NickRe.IsMatch(nick)) return "3 to 16 letters, digits, - or _.";
        if (string.Equals(nick, _myNick, StringComparison.OrdinalIgnoreCase)) return "That's your nickname already.";
        var r = await ServiceClient.NickCheckAsync(nick);
        if (!r.Bool("ok")) return r.Str("error");
        return r.Bool("free") ? "" : (r.Str("reason") is { Length: > 0 } why ? why : "That nickname is taken.");
    }

    private async Task CheckNick(TextBox box, TextBlock hint, Button save)
    {
        _nickCheck?.Cancel();
        var cts = _nickCheck = new CancellationTokenSource();
        var nick = box.Text.Trim();
        save.IsEnabled = false;
        if (nick == "" || nick == _myNick) { hint.Text = ""; return; }
        await Task.Delay(400);
        if (cts.IsCancellationRequested) return;
        var problem = await NickProblem(nick);
        if (cts.IsCancellationRequested) return;
        hint.Text = problem == "" ? $"“{nick}” is free." : problem;
        hint.Foreground = Res(problem == "" ? "SystemFillColorSuccessBrush" : "SystemFillColorCriticalBrush");
        save.IsEnabled = problem == "";
    }

    private void NickBox_TextChanged(object sender, TextChangedEventArgs e) => _ = CheckNick(NickBox, NickHint, NickSave);
    private void NickEdit_TextChanged(object sender, TextChangedEventArgs e) => _ = CheckNick(NickEdit, NickEditHint, NickChange);

    private async Task<bool> ConfirmAndSetNick(string nick, InfoBar errorBar)
    {
        var dlg = new ContentDialog
        {
            XamlRoot = Content.XamlRoot,
            Title = $"Use “{nick}”?",
            Content = "You can change it again in 30 days. Your old nickname stays reserved for you for 30 days.",
            PrimaryButtonText = "Use it",
            CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Primary,
        };
        if (await dlg.ShowAsync() != ContentDialogResult.Primary) return false;
        var r = await ServiceClient.SetNickAsync(nick);
        if (!r.Bool("ok")) { ShowError(errorBar, r.Str("error")); return false; }
        await LoadMe();
        return true;
    }

    private async void NickSave_Click(object sender, RoutedEventArgs e)
    {
        if (await ConfirmAndSetNick(NickBox.Text.Trim(), FindError))
        {
            _dirLoaded = DateTime.MinValue;
            await LoadDirectory();
        }
    }

    private async void NickChange_Click(object sender, RoutedEventArgs e)
    {
        if (await ConfirmAndSetNick(NickEdit.Text.Trim(), ErrorBar)) NickEditHint.Text = "Saved.";
    }

    // ---------------------------------------------------------------- settings and emulators

    private void NameBox_TextChanged(object sender, TextChangedEventArgs e) =>
        SaveName.IsEnabled = NameBox.Text.Trim() != "" && NameBox.Text.Trim() != _savedName;

    private async void SaveName_Click(object sender, RoutedEventArgs e)
    {
        var r = await ServiceClient.SetNameAsync(NameBox.Text.Trim());
        if (r.Bool("ok")) { _savedName = NameBox.Text.Trim(); SaveName.IsEnabled = false; }
        await Poll();
    }

    private void OpenLogs_Click(object sender, RoutedEventArgs e)
    {
        var dir = System.IO.Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.CommonApplicationData), "NetKey");
        try { System.Diagnostics.Process.Start("explorer.exe", Directory.Exists(dir) ? dir : AppContext.BaseDirectory); }
        catch (Exception ex) { App.Log("open logs: " + ex.Message); }
    }

    /// <summary>DOSBox-X loads wpcap.dll from the normal DLL path; Npcap puts it there only in WinPcap API-compatible mode.</summary>
    private void CheckNpcap()
    {
        var sys = Environment.GetFolderPath(Environment.SpecialFolder.System);
        var npcap = File.Exists(System.IO.Path.Combine(sys, "Npcap", "wpcap.dll"));
        var compat = File.Exists(System.IO.Path.Combine(sys, "wpcap.dll"));
        if (compat)
        {
            NpcapChipText.Text = "Npcap ready";
            NpcapChip.Background = Res("SystemFillColorSuccessBackgroundBrush");
            Bar(NpcapBar, false);
            return;
        }
        NpcapChipText.Text = npcap ? "Npcap needs a setting" : "Needs Npcap";
        NpcapChip.Background = Res("SystemFillColorCautionBackgroundBrush");
        NpcapBar.Severity = InfoBarSeverity.Warning;
        NpcapBar.Title = npcap ? "Npcap is installed without WinPcap compatibility" : "Npcap isn't installed";
        NpcapBar.Message = npcap
            ? "DOSBox-X can't find it. Run the Npcap installer again (npcap.com) and tick “Install Npcap in WinPcap API-compatible Mode”."
            : "Install Npcap from npcap.com and tick “Install Npcap in WinPcap API-compatible Mode”. It's free for home use.";
        Bar(NpcapBar, true);
    }

    private static void Copy(string text)
    {
        var p = new DataPackage();
        p.SetText(text);
        Clipboard.SetContent(p);
    }

    private void CopyDosbox_Click(object sender, RoutedEventArgs e) => Copy(DosboxConf.Text.Replace("\n", "\r\n"));

    private void CopySecret_Click(object sender, RoutedEventArgs e) => Copy(_secret);

    private void CopyIpxConf_Click(object sender, RoutedEventArgs e) => Copy("[ipx]\r\nipx=true");

    private void CopyIpxCmd_Click(object sender, RoutedEventArgs e) => Copy("IPXNET CONNECT 127.0.0.1");

    private void CopyNetCfg_Click(object sender, RoutedEventArgs e) => Copy(NetCfg.Text.Replace("\n", "\r\n"));
}
