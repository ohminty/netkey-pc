using System;
using System.IO;
using System.IO.Pipes;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Threading;
using System.Threading.Tasks;

namespace NetKey;

/// <summary>Talks to the NetKey service over its named pipe: one JSON object per line each way.</summary>
public static class ServiceClient
{
    private const string PipeName = "netkey";

    public static async Task<JsonObject> CallAsync(JsonObject request, int timeoutSeconds = 10)
    {
        using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(timeoutSeconds));
        try
        {
            await using var pipe = new NamedPipeClientStream(".", PipeName, PipeDirection.InOut, PipeOptions.Asynchronous);
            await pipe.ConnectAsync(2000, cts.Token);
            var line = Encoding.UTF8.GetBytes(request.ToJsonString() + "\n");
            await pipe.WriteAsync(line, cts.Token);
            await pipe.FlushAsync(cts.Token);
            using var reader = new StreamReader(pipe, Encoding.UTF8, false, 65536, leaveOpen: true);
            var reply = await reader.ReadLineAsync(cts.Token);
            if (reply is null) return Error("The NetKey service closed the connection.");
            return JsonNode.Parse(reply) as JsonObject ?? Error("Bad reply from the NetKey service.");
        }
        catch (TimeoutException)
        {
            return Error("The NetKey service isn't running.", notRunning: true);
        }
        catch (OperationCanceledException)
        {
            return Error("The NetKey service didn't answer in time.");
        }
        catch (Exception e) when (e is IOException or UnauthorizedAccessException or JsonException)
        {
            return Error("Can't talk to the NetKey service: " + e.Message, notRunning: e is IOException);
        }
    }

    private static JsonObject Error(string message, bool notRunning = false) =>
        new() { ["ok"] = false, ["error"] = message, ["not_running"] = notRunning };

    public static Task<JsonObject> StatusAsync() => CallAsync(new JsonObject { ["op"] = "status" }, 5);

    public static Task<JsonObject> JoinAsync(string code, string name) =>
        CallAsync(new JsonObject { ["op"] = "join", ["code"] = code, ["name"] = name }, 40);

    public static Task<JsonObject> LeaveAsync() => CallAsync(new JsonObject { ["op"] = "leave" }, 20);

    public static Task<JsonObject> TestAsync() => CallAsync(new JsonObject { ["op"] = "test" }, 60);

    public static Task<JsonObject> JoinListingAsync(string listing) =>
        CallAsync(new JsonObject { ["op"] = "join_listing", ["listing"] = listing }, 40);

    public static Task<JsonObject> MeAsync() => CallAsync(new JsonObject { ["op"] = "me" }, 20);

    public static Task<JsonObject> DirectoryAsync() => CallAsync(new JsonObject { ["op"] = "directory" }, 20);

    public static Task<JsonObject> NickCheckAsync(string nick) =>
        CallAsync(new JsonObject { ["op"] = "nick_check", ["nick"] = nick }, 15);

    public static Task<JsonObject> SetNickAsync(string nick) =>
        CallAsync(new JsonObject { ["op"] = "set_nick", ["nick"] = nick }, 15);

    public static Task<JsonObject> ChatAsync(long after, int wait) =>
        CallAsync(new JsonObject { ["op"] = "chat", ["after"] = after, ["wait"] = wait }, wait + 20);

    public static Task<JsonObject> ChatSendAsync(string text) =>
        CallAsync(new JsonObject { ["op"] = "chat_send", ["text"] = text }, 15);

    public static Task<JsonObject> ReportAsync(long id, string text) =>
        CallAsync(new JsonObject { ["op"] = "report", ["id"] = id, ["text"] = text }, 15);

    public static Task<JsonObject> SetNameAsync(string name) =>
        CallAsync(new JsonObject { ["op"] = "set_name", ["name"] = name }, 5);
}

internal static class JsonExt
{
    public static string Str(this JsonNode? n, string key) =>
        n?[key] is JsonValue v && v.TryGetValue<string>(out var s) ? s : "";

    public static bool Bool(this JsonNode? n, string key) =>
        n?[key] is JsonValue v && v.TryGetValue<bool>(out var b) && b;

    public static double Num(this JsonNode? n, string key) =>
        n?[key] is JsonValue v && v.TryGetValue<double>(out var d) ? d : 0;
}
