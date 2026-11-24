using Commerce.Application.Shop;
using Commerce.Application.Validation;
using Commerce.Domain;
using Commerce.Domain.Ports;

namespace Commerce.Application.Handlers;

public sealed class OfferHandler(
    IOfferRepository offers,
    IUnitOfWork unitOfWork,
    IPricingClient pricing,
    IOfferRatingReader ratings) : IRequestHandler
{
    public bool CanHandle(string operation) => operation is
        "offers.list" or "offers.get" or "offers.update" or "offers.publish" or "offers.unpublish";

    public async Task<ShopOutcome> HandleAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        if (context.Command.Operation == "offers.get")
        {
            return await GetAsync(context, cancellationToken);
        }

        if (!context.Actor.IsVendor && !context.Actor.IsAdmin)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        if (context.Command.Operation == "offers.list")
        {
            var items = await offers.ListAsync(context.Actor.Id, context.Actor.IsAdmin, cancellationToken);
            return ShopOutcome.Ok(new Dictionary<string, object?>
            {
                ["offers"] = items.Select(EntityMapper.ToDictionary).ToList()
            });
        }

        return await unitOfWork.ExecuteAsync(async token =>
        {
            var offer = await offers.GetAsync(context.Id, token)
                ?? throw new DomainException(DomainError.NotFound());
            if (!context.Actor.IsAdmin && offer.VendorId != context.Actor.Id)
            {
                throw new DomainException(DomainError.Forbidden());
            }

            switch (context.Command.Operation)
            {
                case "offers.publish":
                    if (!offer.IsComplete)
                    {
                        throw new DomainException(DomainError.Bad("INCOMPLETE_OFFER"));
                    }
                    offer.Status = OfferStatus.Published;
                    break;
                case "offers.unpublish":
                    if (!context.Actor.IsAdmin)
                    {
                        throw new DomainException(DomainError.Forbidden());
                    }
                    offer.Status = OfferStatus.Unpublished;
                    break;
                default:
                    var used = offer.Capacity - offer.Available;
                    OfferValidator.ValidateUpdate(context, used);
                    offer.Title = context.Text("title");
                    offer.Description = context.Text("description");
                    offer.City = context.Text("city");
                    offer.Category = context.Text("category");
                    offer.Schedule = context.Text("schedule");
                    offer.PriceCents = context.Number("price_cents");
                    offer.Capacity = (int)context.Number("capacity");
                    offer.Available = offer.Capacity - used;
                    break;
            }

            var current = await pricing.GetCurrentPriceAsync(
                offer.Id, offer.PriceCents, offer.Available, offer.Capacity, token);
            var result = EntityMapper.ToDictionary(offer);
            result["current_price_cents"] = current.PriceCents;
            return ShopOutcome.Ok(new Dictionary<string, object?> { ["offer"] = result });
        }, cancellationToken: cancellationToken);
    }

    private async Task<ShopOutcome> GetAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        var offer = await offers.GetAsync(context.Id, cancellationToken)
            ?? throw new DomainException(DomainError.NotFound());
        var current = await pricing.GetCurrentPriceAsync(
            offer.Id, offer.PriceCents, offer.Available, offer.Capacity, cancellationToken);
        var rating = await ratings.GetAsync(offer.Id, cancellationToken);
        var result = EntityMapper.ToDictionary(offer);
        result["current_price_cents"] = current.PriceCents;
        result["rating"] = new Dictionary<string, object?>
        {
            ["average"] = rating.AverageRating,
            ["reviews_count"] = rating.ReviewsCount
        };
        return ShopOutcome.Ok(new Dictionary<string, object?> { ["offer"] = result });
    }
}
