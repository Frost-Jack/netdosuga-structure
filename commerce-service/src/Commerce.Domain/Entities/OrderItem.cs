namespace Commerce.Domain.Entities;

public sealed class OrderItem
{
    public string OrderId { get; set; } = string.Empty;
    public string OfferId { get; set; } = string.Empty;
    public string VendorId { get; set; } = string.Empty;
    public string Title { get; set; } = string.Empty;
    public int Quantity { get; set; }
    public long PriceCents { get; set; }
    public long TotalCents { get; set; }
    public long DiscountCents { get; set; }
    public long CommissionCents { get; set; }
}
