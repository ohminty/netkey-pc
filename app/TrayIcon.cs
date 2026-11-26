using System;
using System.Runtime.InteropServices;

namespace NetKey;

/// <summary>
/// The notification-area icon (Shell_NotifyIcon on a message-only window). It lives on the UI thread, so WinUI's
/// message loop delivers its messages. Left click opens the window; right click shows Open / Quit.
/// </summary>
public sealed class TrayIcon : IDisposable
{
    private const int WM_APP_TRAY = 0x8000 + 1, WM_LBUTTONUP = 0x0202, WM_RBUTTONUP = 0x0205, WM_CONTEXTMENU = 0x007B;
    private const int NIM_ADD = 0, NIM_MODIFY = 1, NIM_DELETE = 2, NIM_SETVERSION = 4;
    private const int NIF_MESSAGE = 1, NIF_ICON = 2, NIF_TIP = 4, NIF_INFO = 0x10, NIF_SHOWTIP = 0x80;
    private const int CMD_OPEN = 1, CMD_QUIT = 2;
    private static readonly IntPtr HWND_MESSAGE = new(-3);

    private readonly WndProc _proc;   // kept alive: the window class points at it
    private readonly IntPtr _hwnd, _icon;
    private readonly uint _taskbarCreated;
    private bool _added;
    private string _tip = "NetKey";

    public event Action? OpenRequested;
    public event Action? QuitRequested;

    public TrayIcon(string iconPath)
    {
        _proc = Proc;
        var cls = new WNDCLASSEX
        {
            cbSize = Marshal.SizeOf<WNDCLASSEX>(),
            lpfnWndProc = Marshal.GetFunctionPointerForDelegate(_proc),
            hInstance = GetModuleHandle(null),
            lpszClassName = "NetKeyTray",
        };
        RegisterClassEx(ref cls);
        _hwnd = CreateWindowEx(0, "NetKeyTray", "NetKey", 0, 0, 0, 0, 0, HWND_MESSAGE, IntPtr.Zero, cls.hInstance, IntPtr.Zero);
        _icon = LoadImage(IntPtr.Zero, iconPath, 1 /*IMAGE_ICON*/, GetSystemMetrics(49), GetSystemMetrics(50), 0x10 /*LR_LOADFROMFILE*/);
        _taskbarCreated = RegisterWindowMessage("TaskbarCreated");
        Add();
    }

    private NOTIFYICONDATA Data(int flags) => new()
    {
        cbSize = Marshal.SizeOf<NOTIFYICONDATA>(),
        hWnd = _hwnd,
        uID = 1,
        uFlags = flags,
        uCallbackMessage = WM_APP_TRAY,
        hIcon = _icon,
        szTip = _tip,
        uVersion = 4,
    };

    private void Add()
    {
        var d = Data(NIF_MESSAGE | NIF_ICON | NIF_TIP | NIF_SHOWTIP);
        _added = Shell_NotifyIcon(NIM_ADD, ref d);
        if (_added) Shell_NotifyIcon(NIM_SETVERSION, ref d);
    }

    /// <summary>Hover text, e.g. "NetKey · Connected to netkey".</summary>
    public string Tooltip
    {
        set
        {
            var tip = value.Length > 120 ? value[..120] : value;
            if (tip == _tip) return;
            _tip = tip;
            var d = Data(NIF_TIP | NIF_SHOWTIP);
            if (_added) Shell_NotifyIcon(NIM_MODIFY, ref d);
        }
    }

    /// <summary>A Windows notification from the tray icon (new chat message, party ended…).</summary>
    public void Notify(string title, string text)
    {
        var d = Data(NIF_INFO);
        d.szInfoTitle = title.Length > 60 ? title[..60] : title;
        d.szInfo = text.Length > 200 ? text[..200] : text;
        d.dwInfoFlags = 0x4 /*NIIF_USER*/ | 0x20 /*NIIF_LARGE_ICON*/;
        d.hBalloonIcon = _icon;
        if (_added) Shell_NotifyIcon(NIM_MODIFY, ref d);
    }

    private IntPtr Proc(IntPtr hwnd, uint msg, IntPtr wParam, IntPtr lParam)
    {
        if (msg == _taskbarCreated) { Add(); return IntPtr.Zero; }   // Explorer restarted
        if (msg == WM_APP_TRAY)
        {
            var ev = (int)(lParam.ToInt64() & 0xFFFF);
            if (ev == WM_LBUTTONUP) OpenRequested?.Invoke();
            else if (ev == WM_RBUTTONUP || ev == WM_CONTEXTMENU) ShowMenu();
            return IntPtr.Zero;
        }
        return DefWindowProc(hwnd, msg, wParam, lParam);
    }

    private void ShowMenu()
    {
        var menu = CreatePopupMenu();
        AppendMenu(menu, 0, CMD_OPEN, "Open NetKey");
        AppendMenu(menu, 0x800 /*MF_SEPARATOR*/, 0, null);
        AppendMenu(menu, 0, CMD_QUIT, "Quit NetKey");
        SetMenuDefaultItem(menu, CMD_OPEN, 0);
        GetCursorPos(out var pt);
        SetForegroundWindow(_hwnd);
        var cmd = TrackPopupMenuEx(menu, 0x100 /*TPM_RETURNCMD*/ | 0x20 /*TPM_BOTTOMALIGN*/, pt.X, pt.Y, _hwnd, IntPtr.Zero);
        DestroyMenu(menu);
        if (cmd == CMD_OPEN) OpenRequested?.Invoke();
        else if (cmd == CMD_QUIT) QuitRequested?.Invoke();
    }

    public void Dispose()
    {
        if (_added)
        {
            var d = Data(0);
            Shell_NotifyIcon(NIM_DELETE, ref d);
            _added = false;
        }
        if (_icon != IntPtr.Zero) DestroyIcon(_icon);
        if (_hwnd != IntPtr.Zero) DestroyWindow(_hwnd);
    }

    // ---- Win32
    private delegate IntPtr WndProc(IntPtr hwnd, uint msg, IntPtr wParam, IntPtr lParam);

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct WNDCLASSEX
    {
        public int cbSize; public uint style; public IntPtr lpfnWndProc; public int cbClsExtra, cbWndExtra;
        public IntPtr hInstance, hIcon, hCursor, hbrBackground; public string? lpszMenuName; public string lpszClassName; public IntPtr hIconSm;
    }

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct NOTIFYICONDATA
    {
        public int cbSize; public IntPtr hWnd; public int uID, uFlags, uCallbackMessage; public IntPtr hIcon;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 128)] public string szTip;
        public int dwState, dwStateMask;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 256)] public string szInfo;
        public int uVersion;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 64)] public string szInfoTitle;
        public int dwInfoFlags; public Guid guidItem; public IntPtr hBalloonIcon;
    }

    [StructLayout(LayoutKind.Sequential)] private struct POINT { public int X, Y; }

    [DllImport("user32.dll", CharSet = CharSet.Unicode)] private static extern ushort RegisterClassEx(ref WNDCLASSEX c);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] private static extern IntPtr CreateWindowEx(int exStyle, string cls, string name, int style, int x, int y, int w, int h, IntPtr parent, IntPtr menu, IntPtr inst, IntPtr param);
    [DllImport("user32.dll")] private static extern bool DestroyWindow(IntPtr h);
    [DllImport("user32.dll")] private static extern IntPtr DefWindowProc(IntPtr h, uint m, IntPtr w, IntPtr l);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] private static extern uint RegisterWindowMessage(string s);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] private static extern IntPtr LoadImage(IntPtr inst, string name, uint type, int cx, int cy, uint load);
    [DllImport("user32.dll")] private static extern bool DestroyIcon(IntPtr h);
    [DllImport("user32.dll")] private static extern int GetSystemMetrics(int i);
    [DllImport("user32.dll")] private static extern IntPtr CreatePopupMenu();
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] private static extern bool AppendMenu(IntPtr m, uint flags, int id, string? text);
    [DllImport("user32.dll")] private static extern bool SetMenuDefaultItem(IntPtr m, int item, int byPos);
    [DllImport("user32.dll")] private static extern int TrackPopupMenuEx(IntPtr m, uint flags, int x, int y, IntPtr h, IntPtr p);
    [DllImport("user32.dll")] private static extern bool DestroyMenu(IntPtr m);
    [DllImport("user32.dll")] private static extern bool GetCursorPos(out POINT p);
    [DllImport("user32.dll")] private static extern bool SetForegroundWindow(IntPtr h);
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode)] private static extern IntPtr GetModuleHandle(string? name);
    [DllImport("shell32.dll", CharSet = CharSet.Unicode)] private static extern bool Shell_NotifyIcon(int msg, ref NOTIFYICONDATA d);
}
