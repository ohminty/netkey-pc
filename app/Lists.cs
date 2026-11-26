using System;
using Microsoft.UI.Text;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Documents;
using Microsoft.UI.Xaml.Media;

namespace NetKey;

/// <summary>The list rows (players, chat messages, public parties), built in code.</summary>
public sealed partial class MainWindow
{
    private static TextBlock Caption(string text, string brush = "TextFillColorSecondaryBrush") => new()
    {
        Text = text,
        Style = (Style)Application.Current.Resources["CaptionTextBlockStyle"],
        Foreground = Res(brush),
        TextWrapping = TextWrapping.Wrap,
    };

    private static Border Badge(string text) => new()
    {
        CornerRadius = new CornerRadius(4),
        Padding = new Thickness(7, 1, 7, 1),
        VerticalAlignment = VerticalAlignment.Center,
        Background = Res("SubtleFillColorSecondaryBrush"),
        Child = new TextBlock { Text = text, Style = (Style)Application.Current.Resources["CaptionTextBlockStyle"] },
    };

    private static UIElement MemberRow(MemberItem m)
    {
        var g = new Grid { ColumnSpacing = 12, Padding = new Thickness(0, 6, 0, 6) };
        g.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        g.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        g.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        g.Children.Add(new PersonPicture { DisplayName = m.Name, Width = 32, Height = 32 });
        var name = new TextBlock { Text = m.Name, VerticalAlignment = VerticalAlignment.Center, TextTrimming = TextTrimming.CharacterEllipsis };
        Grid.SetColumn(name, 1);
        g.Children.Add(name);
        if (m.Tag != "")
        {
            var b = Badge(m.Tag);
            Grid.SetColumn(b, 2);
            g.Children.Add(b);
        }
        return g;
    }

    private UIElement ChatRow(ChatItem c)
    {
        var head = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        head.Children.Add(new TextBlock
        {
            Text = c.Nick,
            FontWeight = FontWeights.SemiBold,
            Foreground = Res(c.IsHost ? "AccentTextFillColorPrimaryBrush" : "TextFillColorPrimaryBrush"),
        });
        if (c.Badge != "") head.Children.Add(Badge(c.Badge));
        var time = Caption(c.Time, "TextFillColorTertiaryBrush");
        time.VerticalAlignment = VerticalAlignment.Center;
        head.Children.Add(time);
        var row = new StackPanel { Spacing = 2, Padding = new Thickness(18, 6, 18, 6), Tag = c.Id, Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent) };
        row.Children.Add(head);
        row.Children.Add(new TextBlock { Text = c.Text, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true });

        var menu = new MenuFlyout();
        var copy = new MenuFlyoutItem { Text = "Copy text", Icon = new FontIcon { Glyph = "" } };
        copy.Click += (_, _) => Copy(c.Text);
        menu.Items.Add(copy);
        if (!c.IsMe)
        {
            var report = new MenuFlyoutItem { Text = "Report message…", Icon = new FontIcon { Glyph = "" } };
            report.Click += (_, _) => ReportMessage(c);
            menu.Items.Add(report);
        }
        row.ContextFlyout = menu;
        return row;
    }

    private UIElement ListingCard(ListingItem l)
    {
        var info = new StackPanel { Spacing = 4 };
        info.Children.Add(new TextBlock
        {
            Text = l.Title,
            Style = (Style)Application.Current.Resources["SubtitleTextBlockStyle"],
            TextTrimming = TextTrimming.CharacterEllipsis,
        });
        var line = new TextBlock { TextWrapping = TextWrapping.Wrap, Foreground = Res("TextFillColorSecondaryBrush") };
        line.Inlines.Add(new Run { Text = "Hosted by " });
        line.Inlines.Add(new Run { Text = l.Host, FontWeight = FontWeights.SemiBold });
        line.Inlines.Add(new Run { Text = $"  ·  {l.Players}  ·  {l.Ping}" });
        info.Children.Add(line);
        if (l.Games != "")
            info.Children.Add(new TextBlock { Text = l.Games, TextWrapping = TextWrapping.Wrap, Foreground = Res("AccentTextFillColorPrimaryBrush") });
        if (l.Description != "")
            info.Children.Add(new TextBlock
            {
                Text = l.Description, TextWrapping = TextWrapping.Wrap, MaxLines = 3,
                TextTrimming = TextTrimming.CharacterEllipsis, Margin = new Thickness(0, 4, 0, 0),
            });
        if (l.Extra != "") info.Children.Add(Caption(l.Extra, "TextFillColorTertiaryBrush"));

        var join = new Button
        {
            Content = l.JoinLabel, MinWidth = 110, VerticalAlignment = VerticalAlignment.Center, IsEnabled = l.CanJoin,
            Style = (Style)Application.Current.Resources["AccentButtonStyle"],
        };
        join.Click += async (_, _) => await JoinListing(join, l.Id);

        var g = new Grid { ColumnSpacing = 16 };
        g.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        g.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        g.Children.Add(info);
        Grid.SetColumn(join, 1);
        g.Children.Add(join);
        return new Border { Style = (Style)Application.Current.Resources["Card"], Child = g };
    }

    private static UIElement UpcomingCard(ListingItem l)
    {
        var g = new Grid { ColumnSpacing = 16 };
        g.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        g.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        g.Children.Add(new FontIcon { Glyph = "", FontSize = 18, VerticalAlignment = VerticalAlignment.Center, Foreground = Res("TextFillColorSecondaryBrush") });
        var info = new StackPanel { Spacing = 2 };
        info.Children.Add(new TextBlock { Text = l.Title, FontWeight = FontWeights.SemiBold, TextTrimming = TextTrimming.CharacterEllipsis });
        info.Children.Add(new TextBlock
        {
            Text = $"{l.When}  ·  hosted by {l.Host}" + (l.Games != "" ? $"  ·  {l.Games}" : ""),
            TextWrapping = TextWrapping.Wrap, Foreground = Res("TextFillColorSecondaryBrush"),
        });
        Grid.SetColumn(info, 1);
        g.Children.Add(info);
        return new Border { Style = (Style)Application.Current.Resources["Card"], Padding = new Thickness(16, 12, 16, 12), Child = g };
    }
}
