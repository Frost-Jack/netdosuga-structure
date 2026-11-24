namespace Commerce.Domain.Entities;

public sealed class Ticket
{
    public string Code { get; set; } = string.Empty;
    public string OrderId { get; set; } = string.Empty;
    public string OfferId { get; set; } = string.Empty;
    public string VendorId { get; set; } = string.Empty;
    public TicketStatus Status { get; set; } = TicketStatus.Valid;
}
