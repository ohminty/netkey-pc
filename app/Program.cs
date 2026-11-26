using System;
using System.Linq;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.Windows.AppLifecycle;

namespace NetKey;

/// <summary>
/// One NetKey window per user: starting NetKey again (Start menu, desktop) brings the running one forward.
/// "--tray" starts it in the notification area only (sign-in start).
/// </summary>
public static class Program
{
    [STAThread]
    private static int Main(string[] args)
    {
        WinRT.ComWrappersSupport.InitializeComWrappers();
        var main = AppInstance.FindOrRegisterForKey("netkey-main");
        if (!main.IsCurrent)
        {
            // hand our activation to the running app (off the STA thread, as the docs require) and exit
            var activation = AppInstance.GetCurrent().GetActivatedEventArgs();
            Task.Run(() => main.RedirectActivationToAsync(activation).AsTask()).Wait();
            return 0;
        }
        main.Activated += (_, _) => App.Instance?.ShowFromOtherInstance();
        App.StartInTray = args.Contains("--tray");
        Application.Start(p =>
        {
            SynchronizationContext.SetSynchronizationContext(new DispatcherQueueSynchronizationContext(DispatcherQueue.GetForCurrentThread()));
            _ = new App();
        });
        return 0;
    }
}
