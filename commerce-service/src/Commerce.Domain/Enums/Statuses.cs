namespace Commerce.Domain;

public enum ApplicationStatus
{
    Pending,
    Approved,
    Rejected
}

public enum OfferStatus
{
    Draft,
    Published,
    Unpublished
}

public enum OrderStatus
{
    Reserved,
    Paid,
    Cancelled,
    Expired
}

public enum TicketStatus
{
    Valid,
    Redeemed,
    Revoked
}
