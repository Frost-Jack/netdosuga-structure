using System.Buffers;
using System.Diagnostics;
using System.Text;
using System.Text.Json;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Logging.Console;

namespace Commerce.Api.Observability;

[ProviderAlias("CommerceJson")]
public sealed class CommerceJsonConsoleFormatter : ConsoleFormatter
{
    public CommerceJsonConsoleFormatter()
        : base("commerce-json")
    {
    }

    public override void Write<TState>(
        in LogEntry<TState> logEntry,
        IExternalScopeProvider? scopeProvider,
        TextWriter textWriter)
    {
        var message = logEntry.Formatter(logEntry.State, logEntry.Exception);
        if (string.IsNullOrEmpty(message) && logEntry.Exception is null)
        {
            return;
        }

        var activity = Activity.Current;
        var buffer = new ArrayBufferWriter<byte>();
        using (var writer = new Utf8JsonWriter(buffer))
        {
            writer.WriteStartObject();
            writer.WriteString("timestamp", DateTimeOffset.UtcNow.ToString("O"));
            writer.WriteString("level", logEntry.LogLevel.ToString());
            writer.WriteString("service", "commerce-service");
            writer.WriteString("category", logEntry.Category);
            writer.WriteString("message", message);
            writer.WriteString("trace_id", activity?.TraceId.ToString() ?? string.Empty);
            writer.WriteString("span_id", activity?.SpanId.ToString() ?? string.Empty);
            if (logEntry.Exception is not null)
            {
                writer.WriteString("exception", logEntry.Exception.ToString());
            }

            writer.WriteEndObject();
        }

        textWriter.WriteLine(Encoding.UTF8.GetString(buffer.WrittenSpan));
    }
}
