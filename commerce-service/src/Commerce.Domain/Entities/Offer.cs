namespace Commerce.Domain.Entities;

public sealed class Offer
{
    public string Id { get; set; } = string.Empty;
    public string VendorId { get; set; } = string.Empty;
    public string Title { get; set; } = string.Empty;
    public string Description { get; set; } = string.Empty;
    public string City { get; set; } = string.Empty;
    public string Category { get; set; } = string.Empty;
    public string Schedule { get; set; } = string.Empty;
    public string PhotoPath { get; set; } = string.Empty;
    public long PriceCents { get; set; }
    public int Capacity { get; set; }
    public int Available { get; set; }
    public OfferStatus Status { get; set; } = OfferStatus.Draft;
    public DateTimeOffset CreatedAt { get; set; }

    public bool IsComplete =>
        PriceCents > 0 && Capacity > 0 && !string.IsNullOrWhiteSpace(Description);

    public void Reserve(int quantity)
    {
        if (Available < quantity)
        {
            throw new DomainException(DomainError.Conflict("UNAVAILABLE_CAPACITY"));
        }

        Available -= quantity;
    }

    public void Release(int quantity) => Available = Math.Min(Capacity, Available + quantity);
}
