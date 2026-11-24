namespace Commerce.Domain.Entities;

public sealed class OutboxEvent
{
    public string EventId { get; set; } = string.Empty;
    public string Payload { get; set; } = string.Empty;
    public string Headers { get; set; } = "{}";
    public DateTimeOffset CreatedAt { get; set; }
    public DateTimeOffset? PublishedAt { get; set; }
}
