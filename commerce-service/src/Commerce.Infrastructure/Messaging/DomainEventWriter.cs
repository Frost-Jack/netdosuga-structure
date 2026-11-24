using System.Diagnostics;
using System.Text.Json;
using Commerce.Domain.Entities;
using Commerce.Domain.Ports;
using Commerce.Infrastructure.Observability;

namespace Commerce.Infrastructure.Messaging;

public sealed class DomainEventWriter(
    IOutboxRepository outbox,
    IIdentifierGenerator ids,
    IClock clock) : IDomainEventWriter
{
    public Task WriteAsync(string eventType, string orderId, CancellationToken cancellationToken)
    {
        using var enqueueActivity = InfrastructureTelemetry.Source.StartActivity("outbox.enqueue", ActivityKind.Client);
        var eventId = ids.NewId();
        var payload = JsonSerializer.Serialize(new Dictionary<string, object?>
        {
            ["schema_version"] = 1,
            ["demo"] = true,
            ["event_id"] = eventId,
            ["event_type"] = eventType,
            ["order_id"] = orderId,
            ["aggregate_id"] = orderId,
            ["occurred_at"] = clock.UtcNow.ToString("O")
        });
        var headers = new Dictionary<string, string>();
        var activity = Activity.Current;
        if (activity is not null)
        {
            if (activity.Id is not null)
            {
                headers["traceparent"] = activity.Id;
            }

            if (!string.IsNullOrEmpty(activity.TraceStateString))
            {
                headers["tracestate"] = activity.TraceStateString;
            }

            foreach (var item in activity.Baggage)
            {
                headers[item.Key] = item.Value ?? string.Empty;
            }
        }

        outbox.Add(new OutboxEvent
        {
            EventId = eventId,
            Payload = payload,
            Headers = JsonSerializer.Serialize(headers),
            CreatedAt = clock.UtcNow
        });
        return Task.CompletedTask;
    }
}
