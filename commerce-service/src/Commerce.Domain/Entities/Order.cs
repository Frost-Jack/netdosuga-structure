namespace Commerce.Domain.Entities;

public sealed class Order
{
    public string Id { get; set; } = string.Empty;
    public string CustomerId { get; set; } = string.Empty;
    public OrderStatus Status { get; set; } = OrderStatus.Reserved;
    public long TotalCents { get; set; }
    public long DiscountCents { get; set; }
    public long CommissionCents { get; set; }
    public string IdempotencyKey { get; set; } = string.Empty;
    public string? PaymentId { get; set; }
    public string? RefundId { get; set; }
    public DateTimeOffset ExpiresAt { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
}
