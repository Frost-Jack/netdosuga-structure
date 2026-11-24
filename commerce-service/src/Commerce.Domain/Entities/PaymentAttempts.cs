namespace Commerce.Domain.Entities;

public sealed class PaymentAttempt
{
    public string OrderId { get; set; } = string.Empty;
    public long AmountCents { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
}

public sealed class RefundAttempt
{
    public string OrderId { get; set; } = string.Empty;
    public int AttemptCount { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
}
