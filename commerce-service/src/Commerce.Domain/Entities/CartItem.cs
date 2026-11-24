namespace Commerce.Domain.Entities;

public sealed class CartItem
{
    public string CustomerId { get; set; } = string.Empty;
    public string OfferId { get; set; } = string.Empty;
    public int Quantity { get; set; }
}
