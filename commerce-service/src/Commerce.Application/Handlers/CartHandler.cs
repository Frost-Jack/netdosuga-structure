using Commerce.Application.Shop;
using Commerce.Application.Validation;
using Commerce.Domain;
using Commerce.Domain.Entities;
using Commerce.Domain.Ports;

namespace Commerce.Application.Handlers;

public sealed class CartHandler(
    ICartRepository cart,
    IOfferRepository offers,
    IUnitOfWork unitOfWork) : IRequestHandler
{
    public bool CanHandle(string operation) => operation is "cart.get" or "cart.add" or "cart.remove";

    public Task<ShopOutcome> HandleAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        if (!context.Actor.IsCustomer)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        return unitOfWork.ExecuteAsync(async token =>
        {
            if (context.Command.Operation == "cart.add")
            {
                OfferValidator.ValidateQuantity(context);
                var offerId = context.Text("offer_id");
                var offer = await offers.GetAsync(offerId, token)
                    ?? throw new DomainException(DomainError.NotFound());
                if (offer.Status != OfferStatus.Published)
                {
                    throw new DomainException(DomainError.NotFound());
                }

                var quantity = (int)context.Number("quantity");
                if (offer.Available < quantity)
                {
                    throw new DomainException(DomainError.Conflict("UNAVAILABLE_CAPACITY"));
                }

                cart.Upsert(new CartItem { CustomerId = context.Actor.Id, OfferId = offerId, Quantity = quantity });
            }
            else if (context.Command.Operation == "cart.remove")
            {
                cart.Remove(context.Actor.Id, context.Id);
            }

            await unitOfWork.FlushAsync(token);
            var items = await cart.ListAsync(context.Actor.Id, token);
            var enriched = new List<Dictionary<string, object?>>();
            long total = 0;
            foreach (var item in items)
            {
                var offer = await offers.GetAsync(item.OfferId, token);
                if (offer is null)
                {
                    continue;
                }

                var mapped = EntityMapper.ToCartItem(offer, item.Quantity);
                total += offer.PriceCents * item.Quantity;
                enriched.Add(mapped);
            }

            return ShopOutcome.Ok(new Dictionary<string, object?>
            {
                ["cart"] = new Dictionary<string, object?>
                {
                    ["items"] = enriched,
                    ["total_cents"] = total
                }
            });
        }, context.Actor.Id, cancellationToken);
    }
}
