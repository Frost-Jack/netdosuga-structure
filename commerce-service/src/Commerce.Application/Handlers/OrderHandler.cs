using Commerce.Application.Shop;
using Commerce.Application.Validation;
using Commerce.Domain;
using Commerce.Domain.Entities;
using Commerce.Domain.Ports;

namespace Commerce.Application.Handlers;

public sealed class OrderHandler(
    IOrderRepository orders,
    ICartRepository cart,
    IOfferRepository offers,
    IAttemptRepository attempts,
    IUnitOfWork unitOfWork,
    IPricingClient pricing,
    IPaymentClient payment,
    IDomainEventWriter events,
    IClock clock,
    IIdentifierGenerator ids) : IRequestHandler
{
    private static readonly TimeSpan ReservationLifetime = TimeSpan.FromMinutes(15);

    public bool CanHandle(string operation) => operation is "orders.create" or "orders.list" or "orders.get" or "orders.pay" or "orders.cancel";

    public async Task<ShopOutcome> HandleAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        if (!context.Actor.IsCustomer && !context.Actor.IsAdmin)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        return context.Command.Operation switch
        {
            "orders.create" => await CreateAsync(context, cancellationToken),
            "orders.list" => await ListAsync(context, cancellationToken),
            "orders.pay" => await PayAsync(context, cancellationToken),
            "orders.cancel" => await CancelAsync(context, cancellationToken),
            _ => await GetAsync(context, cancellationToken)
        };
    }

    private async Task<ShopOutcome> ListAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        var items = await orders.ListAsync(context.Actor.Id, context.Actor.IsAdmin, cancellationToken);
        return ShopOutcome.Ok(new Dictionary<string, object?>
        {
            ["orders"] = items.Select(EntityMapper.ToDictionary).ToList()
        });
    }

    private Task<ShopOutcome> GetAsync(HandlerContext context, CancellationToken cancellationToken) =>
        LoadOrderAsync(context.Id, context, cancellationToken);

    private Task<ShopOutcome> CreateAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        if (!context.Actor.IsCustomer)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        var key = context.IdempotencyKey;
        if (key.Length is 0 or > 128)
        {
            throw new DomainException(DomainError.Bad("IDEMPOTENCY_KEY_REQUIRED"));
        }

        var bonus = context.Number("bonus_to_spend");
        return unitOfWork.ExecuteAsync(async token =>
        {
            var existing = await orders.GetByIdempotencyKeyAsync(context.Actor.Id, key, token);
            if (existing is not null)
            {
                await unitOfWork.FlushAsync(token);
                return await LoadOrderAsync(existing.Id, context, token);
            }

            var cartItems = await cart.ListAsync(context.Actor.Id, token);
            if (cartItems.Count == 0)
            {
                throw new DomainException(DomainError.Bad("EMPTY_CART"));
            }

            var lines = new List<QuoteLine>();
            var offerById = new Dictionary<string, Offer>(StringComparer.Ordinal);
            foreach (var item in cartItems)
            {
                var offer = await offers.GetAsync(item.OfferId, token)
                    ?? throw new DomainException(DomainError.Bad("EMPTY_CART"));
                if (offer.Status != OfferStatus.Published || offer.Available < item.Quantity)
                {
                    throw new DomainException(DomainError.Conflict("UNAVAILABLE_CAPACITY"));
                }

                offerById[offer.Id] = offer;
                lines.Add(new QuoteLine(offer.Id, offer.PriceCents, item.Quantity, offer.Available, offer.Capacity));
            }

            var quote = await pricing.QuoteAsync(
                context.Actor.Id,
                lines,
                context.Text("promo_code"),
                bonus > 0 ? bonus : 0,
                token);
            var quoteByOffer = quote.Items.ToDictionary(x => x.OfferId, StringComparer.Ordinal);
            var now = clock.UtcNow;
            var order = new Order
            {
                Id = ids.NewId(),
                CustomerId = context.Actor.Id,
                Status = OrderStatus.Reserved,
                TotalCents = quote.TotalCents,
                DiscountCents = quote.DiscountCents,
                CommissionCents = quote.Items.Sum(x => x.TotalCents / 10),
                IdempotencyKey = key,
                ExpiresAt = now.Add(ReservationLifetime),
                CreatedAt = now
            };
            orders.Add(order);

            foreach (var line in lines)
            {
                var offer = offerById[line.OfferId];
                var quoted = quoteByOffer.TryGetValue(line.OfferId, out var value) ? value : new PricingQuoteItem(line.OfferId, line.BasePriceCents, line.BasePriceCents * line.Quantity, 0);
                var item = new OrderItem
                {
                    OrderId = order.Id,
                    OfferId = offer.Id,
                    VendorId = offer.VendorId,
                    Title = offer.Title,
                    Quantity = line.Quantity,
                    PriceCents = quoted.UnitPriceCents,
                    TotalCents = quoted.TotalCents,
                    DiscountCents = quoted.DiscountCents,
                    CommissionCents = quoted.TotalCents / 10
                };
                orders.AddItem(item);
                offer.Reserve(line.Quantity);
            }

            cart.Clear(context.Actor.Id);
            await events.WriteAsync("order.created", order.Id, token);
            await unitOfWork.FlushAsync(token);
            return await LoadOrderAsync(order.Id, context, token);
        }, context.Actor.Id, cancellationToken);
    }

    private async Task<ShopOutcome> PayAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        if (!context.Actor.IsCustomer)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        OfferValidator.ValidateScenario(context);
        await PreparePaymentAsync(context, cancellationToken);
        await EnsureNotExpiredAsync(context.Id, context, cancellationToken);

        return await unitOfWork.ExecuteAsync(async token =>
        {
            var order = await GetOwnedOrderAsync(context, token);
            var pending = await attempts.GetRefundAttemptAsync(order.Id, token);
            if (pending is not null)
            {
                throw new DomainException(DomainError.Conflict("CANCELLATION_PENDING"));
            }

            if (order.Status == OrderStatus.Paid)
            {
                return await LoadOrderAsync(order.Id, context, token);
            }

            if (order.Status != OrderStatus.Reserved)
            {
                throw new DomainException(DomainError.Conflict("INVALID_STATE"));
            }

            var paymentId = await payment.AuthorizeAsync(order.Id, order.TotalCents, context.Text("scenario"), token);
            order.Status = OrderStatus.Paid;
            order.PaymentId = paymentId;

            var items = await orders.ListItemsAsync(order.Id, token);
            order.DiscountCents = items.Sum(x => x.DiscountCents);
            order.CommissionCents = items.Sum(x => x.TotalCents) / 10;
            foreach (var item in items)
            {
                item.CommissionCents = item.TotalCents / 10;
                for (var index = 0; index < item.Quantity; index++)
                {
                    orders.AddTicket(new Ticket
                    {
                        Code = ids.NewId(),
                        OrderId = order.Id,
                        OfferId = item.OfferId,
                        VendorId = item.VendorId,
                        Status = TicketStatus.Valid
                    });
                }
            }

            await pricing.CommitAsync(order.Id, order.CustomerId, order.TotalCents, items.Select(x => new QuoteLine(x.OfferId, x.PriceCents, x.Quantity, 0, 0)).ToList(), token);
            await events.WriteAsync("order.paid", order.Id, token);
            await unitOfWork.FlushAsync(token);
            return await LoadOrderAsync(order.Id, context, token);
        }, context.Actor.Id, cancellationToken);
    }

    private async Task<ShopOutcome> CancelAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        if (!context.Actor.IsCustomer)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        OfferValidator.ValidateScenario(context);
        await PrepareCancelAsync(context, cancellationToken);

        return await unitOfWork.ExecuteAsync(async token =>
        {
            var order = await GetOwnedOrderAsync(context, token);
            if (order.Status == OrderStatus.Cancelled)
            {
                return await LoadOrderAsync(order.Id, context, token);
            }

            if (order.Status is not (OrderStatus.Reserved or OrderStatus.Paid))
            {
                throw new DomainException(DomainError.Conflict("INVALID_STATE"));
            }

            if (order.Status == OrderStatus.Paid)
            {
                try
                {
                    order.RefundId = await payment.RefundAsync(order.Id, order.TotalCents, context.Text("scenario"), token);
                }
                catch (DomainException error) when (error.Error.Code == "MOCK_DECLINED")
                {
                    var attempt = await attempts.GetRefundAttemptAsync(order.Id, token);
                    if (attempt?.AttemptCount == 1)
                    {
                        attempts.DeleteRefundAttempt(order.Id);
                        await unitOfWork.FlushAsync(token);
                    }

                    return ShopOutcome.Error(error.Error.Status, error.Error.Code);
                }
            }
            else if (await attempts.HasPaymentAttemptAsync(order.Id, token))
            {
                await payment.CancelAsync(order.Id, order.TotalCents, token);
            }

            order.Status = OrderStatus.Cancelled;
            orders.RevokeTickets(order.Id);

            var items = await orders.ListItemsAsync(order.Id, token);
            foreach (var item in items)
            {
                var offer = await offers.GetAsync(item.OfferId, token);
                offer?.Release(item.Quantity);
            }

            await pricing.RevertAsync(order.Id, order.CustomerId, order.TotalCents, token);
            await events.WriteAsync("order.cancelled", order.Id, token);
            await unitOfWork.FlushAsync(token);
            return await LoadOrderAsync(order.Id, context, token);
        }, context.Actor.Id, cancellationToken);
    }

    private Task PreparePaymentAsync(HandlerContext context, CancellationToken cancellationToken) =>
        unitOfWork.ExecuteAsync(async token =>
        {
            var order = await GetOwnedOrderAsync(context, token);
            if (order.Status == OrderStatus.Reserved && order.ExpiresAt > clock.UtcNow)
            {
                attempts.AddPaymentAttempt(order.Id, order.TotalCents, clock.UtcNow);
            }

            return true;
        }, context.Actor.Id, cancellationToken);

    private Task PrepareCancelAsync(HandlerContext context, CancellationToken cancellationToken) =>
        unitOfWork.ExecuteAsync(async token =>
        {
            var order = await GetOwnedOrderAsync(context, token);
            if (order.Status == OrderStatus.Paid && await orders.HasRedeemedTicketsAsync(order.Id, token))
            {
                throw new DomainException(DomainError.Conflict("TICKET_ALREADY_REDEEMED"));
            }

            if (order.Status is OrderStatus.Reserved or OrderStatus.Paid)
            {
                await attempts.UpsertRefundAttempt(order.Id, clock.UtcNow, token);
            }

            return true;
        }, context.Actor.Id, cancellationToken);

    private async Task EnsureNotExpiredAsync(string orderId, HandlerContext context, CancellationToken cancellationToken)
    {
        var expired = await unitOfWork.ExecuteAsync(async token =>
        {
            var order = await orders.GetAsync(orderId, token);
            return order is { Status: OrderStatus.Reserved } && order.ExpiresAt <= clock.UtcNow;
        }, cancellationToken: cancellationToken);

        if (!expired)
        {
            return;
        }

        await unitOfWork.ExecuteAsync(async token =>
        {
            var order = await orders.GetAsync(orderId, token)
                ?? throw new DomainException(DomainError.NotFound());
            if (order.Status != OrderStatus.Reserved || order.ExpiresAt > clock.UtcNow)
            {
                return true;
            }

            if (await attempts.HasPaymentAttemptAsync(order.Id, token))
            {
                await payment.CancelAsync(order.Id, order.TotalCents, token);
            }

            var items = await orders.ListItemsAsync(order.Id, token);
            foreach (var item in items)
            {
                var offer = await offers.GetAsync(item.OfferId, token);
                offer?.Release(item.Quantity);
            }

            order.Status = OrderStatus.Expired;
            await events.WriteAsync("order.expired", order.Id, token);
            return true;
        }, cancellationToken: cancellationToken);

        throw new DomainException(DomainError.Conflict("RESERVATION_EXPIRED"));
    }

    private async Task<Order> GetOwnedOrderAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        var order = await orders.GetAsync(context.Id, cancellationToken)
            ?? throw new DomainException(DomainError.NotFound());
        if (order.CustomerId != context.Actor.Id && !context.Actor.IsAdmin)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        if (context.Command.Operation is "orders.pay" or "orders.cancel" && !context.Actor.IsCustomer)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        return order;
    }

    private async Task<ShopOutcome> LoadOrderAsync(string orderId, HandlerContext context, CancellationToken cancellationToken)
    {
        var order = await orders.GetAsync(orderId, cancellationToken)
            ?? throw new DomainException(DomainError.NotFound());
        if (order.CustomerId != context.Actor.Id && !context.Actor.IsAdmin)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        var result = EntityMapper.ToDictionary(order);
        result["items"] = (await orders.ListItemsAsync(order.Id, cancellationToken))
            .Select(EntityMapper.ToDictionary)
            .ToList();
        result["tickets"] = (await orders.ListTicketsAsync(order.Id, cancellationToken))
            .Select(EntityMapper.ToDictionary)
            .ToList();
        return ShopOutcome.Ok(new Dictionary<string, object?> { ["order"] = result });
    }
}
