using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json.Nodes;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;

namespace NetKey;

public sealed class MemberItem
{
    public string Name { get; set; } = "";
    public string Tag { get; set; } = "";
    public Visibility TagVisibility => Tag == "" ? Visibility.Collapsed : Visibility.Visible;
}

public sealed class ChatItem
{
    public long Id { get; set; }
    public string Nick { get; set; } = "";
    public string Text { get; set; } = "";
    public string Time { get; set; } = "";
    public bool IsHost { get; set; }
    public bool IsMe { get; set; }
    public string Badge => IsMe ? "You" : IsHost ? "Host" : "";
    public Visibility BadgeVisibility => Badge == "" ? Visibility.Collapsed : Visibility.Visible;
    public Visibility ReportVisibility => IsMe ? Visibility.Collapsed : Visibility.Visible;

    public static ChatItem From(JsonNode n) => new()
    {
        Id = (long)n.Num("id"),
        Nick = n.Str("nick"),
        Text = n.Str("text"),
        IsHost = n.Bool("host"),
        IsMe = n.Bool("me"),
        Time = DateTimeOffset.FromUnixTimeMilliseconds((long)(n.Num("at") * 1000)).ToLocalTime().ToString("HH:mm"),
    };
}

public sealed class ListingItem
{
    public string Id { get; set; } = "";
    public string Title { get; set; } = "";
    public string Host { get; set; } = "";
    public string Description { get; set; } = "";
    public string Games { get; set; } = "";
    public string Players { get; set; } = "";
    public string Ping { get; set; } = "";
    public string Extra { get; set; } = "";
    public string When { get; set; } = "";
    public bool Full { get; set; }
    public bool Joined { get; set; }
    public bool Mine { get; set; }
    public bool CanJoin { get; set; }
    public string JoinLabel => Joined ? "You're in" : Mine ? "Your party" : Full ? "Full" : "Join";
    public Visibility DescriptionVisibility => Description == "" ? Visibility.Collapsed : Visibility.Visible;
    public Visibility GamesVisibility => Games == "" ? Visibility.Collapsed : Visibility.Visible;

    public static ListingItem From(JsonNode n)
    {
        var games = (n["games"] as JsonArray)?.Select(g => g.Str("name")).Where(s => s != "").ToList() ?? new List<string>();
        var count = (int)n.Num("count");
        var max = (int)n.Num("max");
        var est = n?["est_ms"] is JsonValue v && v.TryGetValue<double>(out var ms) ? $"~{ms:0} ms" : "ping unknown";
        var extra = new List<string>();
        if (n.Bool("auto")) extra.Add("instant join");
        if (n.Str("lang") is { Length: > 0 } lang) extra.Add(lang.ToUpperInvariant());
        if (n.Bool("share")) extra.Add("internet sharing");
        var when = "";
        if (n.Str("state") == "planned" && n?["starts"] is JsonValue sv && sv.TryGetValue<double>(out var starts))
            when = DateTimeOffset.FromUnixTimeSeconds((long)starts).ToLocalTime().ToString("ddd d MMM, HH:mm");
        return new ListingItem
        {
            Id = n.Str("id"),
            Title = n.Str("title"),
            Host = n.Str("host"),
            Description = n.Str("description"),
            Games = string.Join(" · ", games),
            Players = max > 0 ? $"{count} of {max} players" : $"{count} players",
            Ping = est,
            Extra = string.Join(" · ", extra),
            When = when,
            Full = max > 0 && count >= max,
            Joined = n.Bool("joined"),
            Mine = n.Bool("mine"),
        };
    }
}

/// <summary>A numbered setup step: a small accent circle with the number, then the text.</summary>
public sealed class Step : UserControl
{
    private readonly TextBlock _number = new() { HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center, FontSize = 12, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold };
    private readonly TextBlock _text = new() { TextWrapping = TextWrapping.Wrap, VerticalAlignment = VerticalAlignment.Center };

    public Step()
    {
        var circle = new Border
        {
            Width = 24, Height = 24, CornerRadius = new CornerRadius(12), VerticalAlignment = VerticalAlignment.Top,
            Background = (Brush)Application.Current.Resources["AccentFillColorDefaultBrush"], Child = _number,
        };
        _number.Foreground = (Brush)Application.Current.Resources["TextOnAccentFillColorPrimaryBrush"];
        var g = new Grid { ColumnSpacing = 12 };
        g.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        g.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        g.Children.Add(circle);
        Grid.SetColumn(_text, 1);
        g.Children.Add(_text);
        Content = g;
    }

    public string Number { get => _number.Text; set => _number.Text = value; }
    public string Text { get => _text.Text; set => _text.Text = value; }
}
