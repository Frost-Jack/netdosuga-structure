using Commerce.Domain;
using Commerce.Domain.Entities;
using ApplicationEntity = Commerce.Domain.Entities.Application;

namespace Commerce.Application.Shop;

public static class EntityMapper
{
    public static Dictionary<string, object?> ToDictionary(ApplicationEntity application) => new()
    {
        ["id"] = application.Id,
        ["vendor_id"] = application.VendorId,
        ["organization"] = application.Organization,
        ["title"] = application.Title,
        ["city"] = application.City,
        ["category"] = application.Category,
        ["inn"] = application.Inn,
        ["ogrn"] = application.Ogrn,
        ["address"] = application.Address,
        ["contact_person"] = application.ContactPerson,
        ["bik"] = application.Bik,
        ["account"] = application.Account,
        ["status"] = Status(application.Status),
        ["reason"] = application.Reason,
        ["created_at"] = application.CreatedAt
    };

    public static Dictionary<string, object?> ToDictionary(Offer offer) => new()
    {
        ["id"] = offer.Id,
        ["vendor_id"] = offer.VendorId,
        ["title"] = offer.Title,
        ["description"] = offer.Description,
        ["city"] = offer.City,
        ["category"] = offer.Category,
        ["schedule"] = offer.Schedule,
        ["photo_path"] = offer.PhotoPath,
        ["price_cents"] = offer.PriceCents,
        ["capacity"] = offer.Capacity,
        ["available"] = offer.Available,
        ["status"] = Status(offer.Status),
        ["created_at"] = offer.CreatedAt
    };

    public static Dictionary<string, object?> ToCartItem(Offer offer, int quantity)
    {
        var result = ToDictionary(offer);
        result["offer_id"] = offer.Id;
        result["quantity"] = quantity;
        result["total_cents"] = offer.PriceCents * quantity;
        return result;
    }

    public static Dictionary<string, object?> ToDictionary(Order order) => new()
    {
        ["id"] = order.Id,
        ["customer_id"] = order.CustomerId,
        ["status"] = Status(order.Status),
        ["total_cents"] = order.TotalCents,
        ["discount_cents"] = order.DiscountCents,
        ["commission_cents"] = order.CommissionCents,
        ["idempotency_key"] = order.IdempotencyKey,
        ["payment_id"] = order.PaymentId,
        ["refund_id"] = order.RefundId,
        ["expires_at"] = order.ExpiresAt,
        ["created_at"] = order.CreatedAt,
        ["items"] = new List<object?>(),
        ["tickets"] = new List<object?>()
    };

    public static Dictionary<string, object?> ToDictionary(OrderItem item) => new()
    {
        ["order_id"] = item.OrderId,
        ["offer_id"] = item.OfferId,
        ["vendor_id"] = item.VendorId,
        ["title"] = item.Title,
        ["quantity"] = item.Quantity,
        ["price_cents"] = item.PriceCents,
        ["total_cents"] = item.TotalCents,
        ["discount_cents"] = item.DiscountCents,
        ["commission_cents"] = item.CommissionCents
    };

    public static Dictionary<string, object?> ToDictionary(Ticket ticket) => new()
    {
        ["code"] = ticket.Code,
        ["order_id"] = ticket.OrderId,
        ["offer_id"] = ticket.OfferId,
        ["vendor_id"] = ticket.VendorId,
        ["status"] = Status(ticket.Status)
    };

    public static string Status(ApplicationStatus status) => status switch
    {
        ApplicationStatus.Approved => "APPROVED",
        ApplicationStatus.Rejected => "REJECTED",
        _ => "PENDING"
    };

    public static string Status(OfferStatus status) => status switch
    {
        OfferStatus.Published => "PUBLISHED",
        OfferStatus.Unpublished => "UNPUBLISHED",
        _ => "DRAFT"
    };

    public static string Status(OrderStatus status) => status switch
    {
        OrderStatus.Paid => "PAID",
        OrderStatus.Cancelled => "CANCELLED",
        OrderStatus.Expired => "EXPIRED",
        _ => "RESERVED"
    };

    public static string Status(TicketStatus status) => status switch
    {
        TicketStatus.Redeemed => "REDEEMED",
        TicketStatus.Revoked => "REVOKED",
        _ => "VALID"
    };

    public static OrderStatus ParseOrderStatus(string value) => value switch
    {
        "PAID" => OrderStatus.Paid,
        "CANCELLED" => OrderStatus.Cancelled,
        "EXPIRED" => OrderStatus.Expired,
        _ => OrderStatus.Reserved
    };

    public static TicketStatus ParseTicketStatus(string value) => value switch
    {
        "REDEEMED" => TicketStatus.Redeemed,
        "REVOKED" => TicketStatus.Revoked,
        _ => TicketStatus.Valid
    };

    public static ApplicationStatus ParseApplicationStatus(string value) => value switch
    {
        "APPROVED" => ApplicationStatus.Approved,
        "REJECTED" => ApplicationStatus.Rejected,
        _ => ApplicationStatus.Pending
    };

    public static OfferStatus ParseOfferStatus(string value) => value switch
    {
        "PUBLISHED" => OfferStatus.Published,
        "UNPUBLISHED" => OfferStatus.Unpublished,
        _ => OfferStatus.Draft
    };
}
