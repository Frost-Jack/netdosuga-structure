using Commerce.Application.Shop;
using Commerce.Application.Validation;
using Commerce.Domain;
using Commerce.Domain.Entities;
using Commerce.Domain.Ports;
using ApplicationEntity = Commerce.Domain.Entities.Application;

namespace Commerce.Application.Handlers;

public sealed class ApplicationHandler(
    IApplicationRepository applications,
    IOfferRepository offers,
    IUnitOfWork unitOfWork,
    IClock clock,
    IIdentifierGenerator ids,
    IDomainEventWriter events) : IRequestHandler
{
    public bool CanHandle(string operation) => operation is "applications.list" or "applications.create" or "applications.approve" or "applications.reject";

    public async Task<ShopOutcome> HandleAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        if (!context.Actor.IsVendor && !context.Actor.IsAdmin)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        switch (context.Command.Operation)
        {
            case "applications.list":
                return await ListAsync(context, cancellationToken);
            case "applications.create":
                return await CreateAsync(context, cancellationToken);
            default:
                return await DecideAsync(context, cancellationToken);
        }
    }

    private async Task<ShopOutcome> ListAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        var items = await applications.ListAsync(context.Actor.Id, context.Actor.IsAdmin, cancellationToken);
        return ShopOutcome.Ok(new Dictionary<string, object?>
        {
            ["applications"] = items.Select(EntityMapper.ToDictionary).ToList()
        });
    }

    private Task<ShopOutcome> CreateAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        if (!context.Actor.IsVendor)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        ApplicationFieldsValidator.Validate(context);
        return unitOfWork.ExecuteAsync(async token =>
        {
            var application = new ApplicationEntity
            {
                Id = ids.NewId(),
                VendorId = context.Actor.Id,
                Organization = context.Text("organization"),
                Title = context.Text("title"),
                City = context.Text("city"),
                Category = context.Text("category"),
                Inn = context.Text("inn"),
                Ogrn = context.Text("ogrn"),
                Address = context.Text("address"),
                ContactPerson = context.Text("contact_person"),
                Bik = context.Text("bik"),
                Account = context.Text("account"),
                CreatedAt = clock.UtcNow
            };
            applications.Add(application);
            await Task.CompletedTask;
            return ShopOutcome.Created(new Dictionary<string, object?>
            {
                ["application"] = EntityMapper.ToDictionary(application)
            });
        }, cancellationToken: cancellationToken);
    }

    private Task<ShopOutcome> DecideAsync(HandlerContext context, CancellationToken cancellationToken)
    {
        if (!context.Actor.IsAdmin)
        {
            throw new DomainException(DomainError.Forbidden());
        }

        var approve = context.Command.Operation == "applications.approve";
        if (!approve)
        {
            OfferValidator.ValidateReason(context);
        }

        return unitOfWork.ExecuteAsync(async token =>
        {
            var application = await applications.GetAsync(context.Id, token)
                ?? throw new DomainException(DomainError.NotFound());
            if (application.Status != ApplicationStatus.Pending)
            {
                throw new DomainException(DomainError.Conflict("INVALID_STATE"));
            }

            application.Status = approve ? ApplicationStatus.Approved : ApplicationStatus.Rejected;
            application.Reason = approve ? string.Empty : context.Text("reason");
            await events.WriteAsync(approve ? "application.approved" : "application.rejected", application.Id, token);
            var result = new Dictionary<string, object?>
            {
                ["application"] = EntityMapper.ToDictionary(application)
            };

            if (approve)
            {
                var offer = new Offer
                {
                    Id = ids.NewId(),
                    VendorId = application.VendorId,
                    Title = application.Title,
                    City = application.City,
                    Category = application.Category,
                    CreatedAt = clock.UtcNow
                };
                offers.Add(offer);
                result["offer"] = EntityMapper.ToDictionary(offer);
            }

            return ShopOutcome.Ok(result);
        }, cancellationToken: cancellationToken);
    }
}
