using Commerce.Application.Shop;
using Commerce.Domain;

namespace Commerce.Application.Validation;

public static class OfferValidator
{
    public static void ValidateUpdate(HandlerContext context, int usedSeats)
    {
        foreach (var key in new[] { "title", "description", "city", "category" })
        {
            var value = context.Text(key);
            if (value.Length is 0 or > 300)
            {
                throw new DomainException(DomainError.Bad("REQUIRED_FIELDS"));
            }
        }

        var schedule = context.Text("schedule");
        if (schedule.Length > 1000)
        {
            throw new DomainException(DomainError.Bad("INVALID_SCHEDULE"));
        }

        var price = context.Number("price_cents");
        var capacity = context.Number("capacity");
        if (price is < 1 or > 100_000_000 || capacity < usedSeats || capacity > 100_000)
        {
            throw new DomainException(DomainError.Bad("INVALID_AMOUNT_OR_CAPACITY"));
        }
    }

    public static void ValidateReason(HandlerContext context)
    {
        var reason = context.Text("reason");
        if (reason.Length is 0 or > 300)
        {
            throw new DomainException(DomainError.Bad("REASON_REQUIRED"));
        }
    }

    public static void ValidateQuantity(HandlerContext context)
    {
        var quantity = context.Number("quantity");
        if (quantity is < 1 or > 20)
        {
            throw new DomainException(DomainError.Bad("INVALID_QUANTITY"));
        }
    }

    public static void ValidateScenario(HandlerContext context)
    {
        var scenario = context.Text("scenario");
        if (scenario is not ("" or "success" or "error" or "delay" or "timeout"))
        {
            throw new DomainException(DomainError.Bad("INVALID_SCENARIO"));
        }
    }
}
