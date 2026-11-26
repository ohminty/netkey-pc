using System;
using System.IO;
using System.Text.Json;
using Microsoft.UI.Xaml;

namespace NetKey;

/// <summary>What the person chose the close button does (stored per user).</summary>
public sealed class AppPrefs
{
    public string OnClose { get; set; } = "ask";   // "ask" (first close), "tray" or "quit"

    private static string PathOf =>
        System.IO.Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "NetKey", "app.json");

    public static AppPrefs Load()
    {
        try { return JsonSerializer.Deserialize<AppPrefs>(File.ReadAllText(PathOf)) ?? new AppPrefs(); }
        catch (Exception e) when (e is IOException or JsonException or UnauthorizedAccessException) { return new AppPrefs(); }
    }

    public void Save()
    {
        try
        {
            Directory.CreateDirectory(System.IO.Path.GetDirectoryName(PathOf)!);
            File.WriteAllText(PathOf, JsonSerializer.Serialize(this));
        }
        catch (Exception e) when (e is IOException or UnauthorizedAccessException) { App.Log("prefs: " + e.Message); }
    }
}

public partial class App : Application
{
    private MainWindow? _window;

    public static App? Instance { get; private set; }
    public static bool StartInTray { get; set; }

    public static string LogPath =>
        System.IO.Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "NetKey", "app.log");

    public static void Log(string message)
    {
        try
        {
            Directory.CreateDirectory(System.IO.Path.GetDirectoryName(LogPath)!);
            File.AppendAllText(LogPath, $"{DateTime.Now:yyyy-MM-dd HH:mm:ss} {message}{Environment.NewLine}");
        }
        catch (IOException) { }
    }

    public App()
    {
        Instance = this;
        InitializeComponent();
        UnhandledException += (_, e) => Log("unhandled: " + e.Exception);
        AppDomain.CurrentDomain.UnhandledException += (_, e) => Log("unhandled (domain): " + e.ExceptionObject);
    }

    protected override void OnLaunched(LaunchActivatedEventArgs args)
    {
        try
        {
            _window = new MainWindow();
            if (!StartInTray) _window.Activate();
        }
        catch (Exception e)
        {
            Log("startup: " + e);
            throw;
        }
    }

    /// <summary>Someone started NetKey again: bring this window forward (called off the UI thread).</summary>
    public void ShowFromOtherInstance() => _window?.DispatcherQueue.TryEnqueue(() => _window.ShowWindow());
}
